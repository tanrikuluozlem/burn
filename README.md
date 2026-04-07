# burn

[![CI](https://github.com/ozlemtanrikulu/burn/actions/workflows/ci.yml/badge.svg)](https://github.com/ozlemtanrikulu/burn/actions/workflows/ci.yml)

Your Kubernetes cluster is burning money. Find out where.

## Why

Running Kubernetes in production gets expensive fast. Most teams overprovision by 40-60% without realizing it. `burn` identifies exactly which nodes are wasting money and tells you what to do about it.

## Features

- Per-node cost breakdown (hourly/monthly)
- Waste detection for underutilized resources
- Optimization recommendations via Claude
- Multi-cloud pricing: AWS, Azure (spot aware)
- Slack reports for daily cost updates
- Prometheus integration for real usage metrics

## Install

```bash
go install github.com/ozlemtanrikulu/burn/cmd/burn@latest
```

## Quick Start

```bash
# Basic analysis
burn analyze

# With AI recommendations
burn analyze --ai

# Send report to Slack
burn report --ai
```

## Configuration

Environment variables:

| Variable | Description | Required |
|----------|-------------|----------|
| `PROMETHEUS_URL` | Prometheus server URL | No |
| `ANTHROPIC_API_KEY` | Claude API key (for --ai) | For AI |
| `SLACK_WEBHOOK_URL` | Slack webhook (for report) | For Slack |

## Usage

```bash
# Analyze specific namespace
burn analyze -n production

# JSON output
burn analyze -o json

# Verbose mode
burn analyze -v
```

## Sample Output

```
Cluster Cost Analysis - 2024-01-15T09:00:00Z
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

Nodes: 3 | Pods: 47
Hourly: $0.7200 | Monthly: $525.60

NODE                  TYPE        SPOT  PODS  CPU%  MEM%  HOURLY    MONTHLY
────                  ────        ────  ────  ────  ────  ──────    ───────
ip-10-0-1-101         m5.large    yes   8     45%   52%   $0.0500   $36.50
ip-10-0-1-102         m5.xlarge   no    12    68%   72%   $0.1920   $140.16
ip-10-0-1-103         m5.large    yes   3     12%   8%    $0.0500   $36.50

Waste Analysis:
  Underutilized: 1 nodes
  Potential savings: $25.55/mo

  - ip-10-0-1-103 (12%): Very low utilization - consider smaller instance type
```

## How it Works

```
K8s API → Collector → Analyzer → Advisor (Claude) → Slack
              ↓            ↓
         Prometheus    Pricing API
         (optional)    (AWS/Azure)
```

## Deployment

Build and push to your registry:

```bash
docker build -t your-registry/burn:latest .
docker push your-registry/burn:latest
```

### CronJob

Daily cost reports at 9 AM UTC:

```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: burn-report
spec:
  schedule: "0 9 * * *"
  jobTemplate:
    spec:
      template:
        spec:
          containers:
          - name: burn
            image: your-registry/burn:latest
            args: ["report", "--ai"]
            envFrom:
            - secretRef:
                name: burn-secrets
          restartPolicy: OnFailure
```

### Helm Values

```yaml
# values.yaml
schedule: "0 9 * * *"
prometheus:
  url: "http://prometheus-kube-prometheus-prometheus.monitoring:9090"
secrets:
  existingSecret: "burn-secrets"
```

## Development

```bash
# Build
make build

# Test
make test

# Lint
make lint
```

## License

Apache 2.0 - See [LICENSE](LICENSE) for details.
