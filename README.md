# burn

Kubernetes cost analysis with AI-powered recommendations.

## What it does

- Analyzes cluster costs (per-node, hourly/monthly)
- Detects underutilized resources and waste
- AI-powered optimization recommendations
- Slack integration for automated reports
- Works with AWS, GCP, Azure (spot aware)

## Install

```bash
go install github.com/ozlemtanrikulu/burn/cmd/burn@latest
```

## Usage

```bash
burn analyze                    # analyze current cluster
burn analyze -n production      # specific namespace
burn analyze -o json            # json output
burn analyze --ai               # with AI recommendations
burn analyze --slack            # send to slack
```

## Output

```
Cluster Cost Analysis

Summary:
  Nodes: 5 | Pods: 47
  Hourly Cost:  $1.82
  Monthly Cost: $1328.60

NODE                  TYPE              SPOT  PODS  CPU%  MEM%  HOURLY   MONTHLY
node-1                n2-standard-4     no    12    68%   72%   $0.48    $350.40
node-2                m5.large          yes   8     45%   52%   $0.05    $36.50
node-3                Standard_D4s_v3   no    3     12%   8%    $0.19    $138.70

Waste Analysis:
  Underutilized Nodes: 1
  Potential Monthly Savings: $97.09
```
