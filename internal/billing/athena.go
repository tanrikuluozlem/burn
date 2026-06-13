package billing

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/athena"
	athenatypes "github.com/aws/aws-sdk-go-v2/service/athena/types"
)

type AthenaClient struct {
	client *athena.Client
	config AthenaConfig
}

func NewAthenaClient(ctx context.Context, cfg AthenaConfig) (*AthenaClient, error) {
	if err := ValidateAthenaConfig(cfg); err != nil {
		return nil, err
	}

	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}

	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}

	return &AthenaClient{
		client: athena.NewFromConfig(awsCfg),
		config: cfg,
	}, nil
}

func (a *AthenaClient) DetectColumns(ctx context.Context) (CURColumnSet, error) {
	sql := fmt.Sprintf("SHOW COLUMNS IN %s.%s", a.config.Database, a.config.Table)

	rows, _, err := a.executeQuery(ctx, sql)
	if err != nil {
		return CURColumnSet{}, fmt.Errorf("column detection: %w", err)
	}

	var colSet CURColumnSet
	for _, row := range rows {
		if len(row) == 0 {
			continue
		}
		col := strings.TrimSpace(row[0])
		switch {
		case col == "reservation_reservation_a_r_n":
			colSet.HasReservationARN = true
		case col == "savings_plan_savings_plan_a_r_n":
			colSet.HasSavingsPlanARN = true
		case col == "reservation_effective_cost":
			colSet.HasEffectiveCost = true
		case col == "savings_plan_savings_plan_effective_cost":
			colSet.HasEffectiveCost = true
		case strings.HasPrefix(col, "split_line_item"):
			colSet.HasSplitLineItem = true
		}
	}

	return colSet, nil
}

type QueryResult struct {
	Items        []CURLineItem
	ScannedBytes int64
	DaysQueried  int
	DaysFailed   int
}

func (a *AthenaClient) QueryCURForPeriod(ctx context.Context, start, end time.Time, colSet CURColumnSet) (*QueryResult, error) {
	result := &QueryResult{}

	type dayResult struct {
		items   []CURLineItem
		scanned int64
		err     error
		day     string
	}

	var days []time.Time
	current := start
	for current.Before(end) {
		days = append(days, current)
		current = current.AddDate(0, 0, 1)
	}

	results := make([]dayResult, len(days))
	sem := make(chan struct{}, 10) // max 10 concurrent Athena queries
	var wg sync.WaitGroup

	for i, day := range days {
		wg.Add(1)
		go func(idx int, dayStart time.Time) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			dayEnd := dayStart.AddDate(0, 0, 1)
			if dayEnd.After(end) {
				dayEnd = end
			}

			sql := a.buildDayQuery(dayStart, dayEnd, colSet)
			rows, scanned, err := a.executeQuery(ctx, sql)
			if err != nil {
				results[idx] = dayResult{err: err, day: dayStart.Format("2006-01-02")}
				return
			}

			items := parseCURRows(rows, colSet)
			results[idx] = dayResult{items: items, scanned: scanned, day: dayStart.Format("2006-01-02")}
		}(i, day)
	}

	wg.Wait()

	for _, dr := range results {
		if dr.err != nil {
			slog.Warn("athena query failed for day", "day", dr.day, "err", dr.err)
			result.DaysFailed++
			continue
		}
		result.DaysQueried++
		result.ScannedBytes += dr.scanned
		result.Items = append(result.Items, dr.items...)
		slog.Debug("queried CUR day", "day", dr.day, "rows", len(dr.items))
	}

	if result.DaysQueried == 0 && result.DaysFailed > 0 {
		return nil, fmt.Errorf("all %d daily CUR queries failed", result.DaysFailed)
	}

	return result, nil
}

func (a *AthenaClient) buildDayQuery(dayStart, dayEnd time.Time, colSet CURColumnSet) string {
	costExpr := "line_item_unblended_cost"
	if colSet.HasEffectiveCost {
		var cases []string
		if colSet.HasSavingsPlanARN {
			cases = append(cases, "WHEN 'SavingsPlanCoveredUsage' THEN savings_plan_savings_plan_effective_cost")
		}
		if colSet.HasReservationARN {
			cases = append(cases, "WHEN 'DiscountedUsage' THEN reservation_effective_cost")
		}
		if len(cases) > 0 {
			costExpr = fmt.Sprintf("CASE line_item_line_item_type %s ELSE line_item_unblended_cost END",
				strings.Join(cases, " "))
		}
	}

	cols := []string{
		"line_item_resource_id",
		"line_item_usage_type",
		"line_item_usage_start_date",
		"line_item_usage_amount",
		"line_item_unblended_cost",
		fmt.Sprintf("%s AS effective_cost", costExpr),
		"pricing_term",
		"product_instance_type",
		"product_region_code",
		"line_item_line_item_type",
	}

	if colSet.HasReservationARN {
		cols = append(cols, "reservation_reservation_a_r_n")
	}
	if colSet.HasSavingsPlanARN {
		cols = append(cols, "savings_plan_savings_plan_a_r_n")
	}

	lineItemTypes := "'Usage', 'DiscountedUsage', 'SavingsPlanCoveredUsage'"

	return fmt.Sprintf(
		`SELECT %s FROM %s.%s
WHERE line_item_product_code = 'AmazonEC2'
  AND line_item_resource_id LIKE 'i-%%'
  AND line_item_usage_start_date >= TIMESTAMP '%s'
  AND line_item_usage_start_date < TIMESTAMP '%s'
  AND line_item_line_item_type IN (%s)`,
		strings.Join(cols, ", "),
		a.config.Database, a.config.Table,
		dayStart.Format("2006-01-02 15:04:05"),
		dayEnd.Format("2006-01-02 15:04:05"),
		lineItemTypes,
	)
}

func (a *AthenaClient) QuerySplitCostAllocation(ctx context.Context, start, end time.Time) (map[string]float64, error) {
	sql := fmt.Sprintf(
		`SELECT tags['aws:eks:namespace'] AS ns,
		        SUM(split_line_item_split_cost) AS cost
		 FROM %s.%s
		 WHERE split_line_item_split_cost IS NOT NULL
		   AND tags['aws:eks:namespace'] IS NOT NULL
		   AND tags['aws:eks:namespace'] != ''
		   AND line_item_usage_start_date >= TIMESTAMP '%s'
		   AND line_item_usage_start_date < TIMESTAMP '%s'
		 GROUP BY tags['aws:eks:namespace']`,
		a.config.Database, a.config.Table,
		start.Format("2006-01-02 15:04:05"),
		end.Format("2006-01-02 15:04:05"),
	)

	rows, _, err := a.executeQuery(ctx, sql)
	if err != nil {
		return nil, err
	}

	nsCosts := make(map[string]float64)
	for _, row := range rows {
		if len(row) >= 2 && row[0] != "" {
			nsCosts[row[0]] = parseFloat(row[1])
		}
	}
	return nsCosts, nil
}

// QueryNonComputeCosts fetches disk, LB, public IP, and EKS management costs in parallel.
func (a *AthenaClient) QueryNonComputeCosts(ctx context.Context, start, end time.Time) (disk, lb, ip []CURLineItem, eksCost float64, scanned int64, err error) {
	startTS := start.Format("2006-01-02 15:04:05")
	endTS := end.Format("2006-01-02 15:04:05")
	db := a.config.Database
	table := a.config.Table

	type queryResult struct {
		items   []CURLineItem
		cost    float64
		scanned int64
		err     error
	}

	queries := map[string]string{
		"disk": fmt.Sprintf(
			`SELECT line_item_resource_id, line_item_usage_type, line_item_usage_amount,
			        line_item_unblended_cost, product_region_code
			 FROM %s.%s
			 WHERE line_item_product_code = 'AmazonEC2'
			   AND line_item_resource_id LIKE 'vol-%%'
			   AND line_item_usage_start_date >= TIMESTAMP '%s'
			   AND line_item_usage_start_date < TIMESTAMP '%s'
			   AND line_item_line_item_type IN ('Usage', 'DiscountedUsage', 'SavingsPlanCoveredUsage')`,
			db, table, startTS, endTS),
		"lb": fmt.Sprintf(
			`SELECT line_item_resource_id, line_item_usage_type, line_item_usage_amount,
			        line_item_unblended_cost, product_region_code
			 FROM %s.%s
			 WHERE line_item_product_code = 'AWSELB'
			   AND line_item_usage_start_date >= TIMESTAMP '%s'
			   AND line_item_usage_start_date < TIMESTAMP '%s'
			   AND line_item_line_item_type = 'Usage'`,
			db, table, startTS, endTS),
		"ip": fmt.Sprintf(
			`SELECT line_item_resource_id, line_item_usage_type, line_item_usage_amount,
			        line_item_unblended_cost, product_region_code
			 FROM %s.%s
			 WHERE line_item_product_code = 'AmazonEC2'
			   AND line_item_usage_type LIKE '%%ElasticIP%%'
			   AND line_item_usage_start_date >= TIMESTAMP '%s'
			   AND line_item_usage_start_date < TIMESTAMP '%s'`,
			db, table, startTS, endTS),
		"eks": fmt.Sprintf(
			`SELECT SUM(line_item_unblended_cost) AS cost
			 FROM %s.%s
			 WHERE line_item_product_code = 'AmazonEKS'
			   AND line_item_usage_start_date >= TIMESTAMP '%s'
			   AND line_item_usage_start_date < TIMESTAMP '%s'`,
			db, table, startTS, endTS),
	}

	results := make(map[string]*queryResult)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for name, sql := range queries {
		wg.Add(1)
		go func(name, sql string) {
			defer wg.Done()
			rows, s, qErr := a.executeQuery(ctx, sql)
			mu.Lock()
			results[name] = &queryResult{scanned: s, err: qErr}
			if qErr == nil {
				results[name].items = parseSimpleCostRows(rows, name)
				if name == "eks" && len(rows) > 0 && len(rows[0]) > 0 {
					results[name].cost = parseFloat(rows[0][0])
				}
			}
			mu.Unlock()
		}(name, sql)
	}

	wg.Wait()

	for name, r := range results {
		scanned += r.scanned
		if r.err != nil {
			slog.Warn("non-compute CUR query failed", "type", name, "err", r.err)
			continue
		}
	}

	if r := results["disk"]; r != nil && r.err == nil {
		disk = r.items
	}
	if r := results["lb"]; r != nil && r.err == nil {
		lb = r.items
	}
	if r := results["ip"]; r != nil && r.err == nil {
		ip = r.items
	}
	if r := results["eks"]; r != nil && r.err == nil {
		eksCost = r.cost
	}

	return disk, lb, ip, eksCost, scanned, nil
}

func parseSimpleCostRows(rows [][]string, category string) []CURLineItem {
	cat := CategoryOther
	switch category {
	case "disk":
		cat = CategoryDisk
	case "lb":
		cat = CategoryNetwork
	case "ip":
		cat = CategoryNetwork
	}

	var items []CURLineItem
	for _, row := range rows {
		if len(row) < 4 {
			continue
		}
		items = append(items, CURLineItem{
			ResourceID:    row[0],
			UsageType:     row[1],
			UsageAmount:   parseFloat(row[2]),
			EffectiveCost: parseFloat(row[3]),
			Category:      cat,
		})
	}
	return items
}

func (a *AthenaClient) executeQuery(ctx context.Context, sql string) ([][]string, int64, error) {
	workgroup := a.config.WorkGroup
	if workgroup == "" {
		workgroup = "primary"
	}

	input := &athena.StartQueryExecutionInput{
		QueryString: aws.String(sql),
		QueryExecutionContext: &athenatypes.QueryExecutionContext{
			Database: aws.String(a.config.Database),
		},
		ResultConfiguration: &athenatypes.ResultConfiguration{
			OutputLocation: aws.String(a.config.OutputLocation),
		},
		WorkGroup: aws.String(workgroup),
	}

	startResult, err := a.client.StartQueryExecution(ctx, input)
	if err != nil {
		return nil, 0, fmt.Errorf("start query: %w", err)
	}

	executionID := *startResult.QueryExecutionId

	if err := a.waitForQuery(ctx, executionID); err != nil {
		return nil, 0, err
	}

	var rows [][]string
	var scanned int64
	var nextToken *string

	for {
		resultInput := &athena.GetQueryResultsInput{
			QueryExecutionId: aws.String(executionID),
			NextToken:        nextToken,
		}

		result, err := a.client.GetQueryResults(ctx, resultInput)
		if err != nil {
			return nil, 0, fmt.Errorf("get results: %w", err)
		}

		for i, row := range result.ResultSet.Rows {
			if i == 0 && nextToken == nil {
				continue
			}
			var vals []string
			for _, datum := range row.Data {
				if datum.VarCharValue != nil {
					vals = append(vals, *datum.VarCharValue)
				} else {
					vals = append(vals, "")
				}
			}
			rows = append(rows, vals)
		}

		nextToken = result.NextToken
		if nextToken == nil {
			break
		}
	}

	exec, err := a.client.GetQueryExecution(ctx, &athena.GetQueryExecutionInput{
		QueryExecutionId: aws.String(executionID),
	})
	if err == nil && exec.QueryExecution.Statistics != nil && exec.QueryExecution.Statistics.DataScannedInBytes != nil {
		scanned = *exec.QueryExecution.Statistics.DataScannedInBytes
	}

	return rows, scanned, nil
}

func (a *AthenaClient) waitForQuery(ctx context.Context, executionID string) error {
	for {
		result, err := a.client.GetQueryExecution(ctx, &athena.GetQueryExecutionInput{
			QueryExecutionId: aws.String(executionID),
		})
		if err != nil {
			return fmt.Errorf("get query execution: %w", err)
		}

		state := result.QueryExecution.Status.State
		switch state {
		case athenatypes.QueryExecutionStateSucceeded:
			return nil
		case athenatypes.QueryExecutionStateFailed:
			reason := ""
			if result.QueryExecution.Status.StateChangeReason != nil {
				reason = *result.QueryExecution.Status.StateChangeReason
			}
			return fmt.Errorf("athena query failed: %s", reason)
		case athenatypes.QueryExecutionStateCancelled:
			return fmt.Errorf("athena query cancelled")
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func parseCURRows(rows [][]string, colSet CURColumnSet) []CURLineItem {
	var items []CURLineItem
	for _, row := range rows {
		if len(row) < 10 {
			continue
		}

		item := CURLineItem{
			ResourceID:    row[0],
			UsageType:     row[1],
			UsageAmount:   parseFloat(row[3]),
			EffectiveCost: parseFloat(row[5]),
			PricingTerm:   row[6],
			InstanceType:  row[7],
			Region:        row[8],
		}

		idx := 10
		if colSet.HasReservationARN && idx < len(row) {
			item.ReservationARN = row[idx]
			idx++
		}
		if colSet.HasSavingsPlanARN && idx < len(row) {
			item.SavingsPlanARN = row[idx]
		}

		if item.ResourceID != "" {
			items = append(items, item)
		}
	}
	return items
}

func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}
