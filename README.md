# burn

[![CI](https://github.com/tanrikuluozlem/burn/actions/workflows/ci.yml/badge.svg)](https://github.com/tanrikuluozlem/burn/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/tanrikuluozlem/burn)](https://github.com/tanrikuluozlem/burn/releases)

Your Kubernetes cluster is burning money. Find out where.

```
NAMESPACES
──────────
NAMESPACE            PODS  CPU REQ→USED  MEM REQ→USED   COST/MO
argocd               4     2.0 → 25m     2.0Gi → 390Mi  $46
kube-system          21    1.4 → 47m     1.6Gi → 752Mi  $34
monitoring           11    1.6 → 73m     829Mi → 1.4Gi  $33
app-api-qa           3     600m → 5m     768Mi → 91Mi   $15
app-api-dev          3     600m → 4m     768Mi → 197Mi  $15
app-api-prod         2     400m → 4m     512Mi → 17Mi   $10
app-web-prod         2     400m → <1m    512Mi → 9Mi    $10
Idle (unallocated)                                     $168
─────────────────────────────────────────────────────────
Total                                                  $350
```

One command. No setup. No dashboard. Just answers.

## What it does

- **Namespace cost breakdown** with request vs actual usage
- **AI recommendations** — rightsizing, spot migration, with copy-paste kubectl commands
- **Slack bot** — `/burn` for cost reports, `/burn ask "..."` for natural language questions
- **Time-based analysis** — `--period 7d` for weekly averages instead of point-in-time snapshots
- **Multi-cloud pricing** — AWS, Azure, GCP with weekly auto-updates

## Install

```bash
# Homebrew
brew install tanrikuluozlem/burn/burn

# Binary
curl -L https://github.com/tanrikuluozlem/burn/releases/latest/download/burn_$(uname -s | tr '[:upper:]' '[:lower:]')_$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/').tar.gz | tar xz

# Docker
docker pull ghcr.io/tanrikuluozlem/burn:latest

# Go
go install github.com/tanrikuluozlem/burn/cmd/burn@latest
```

> **macOS:** If you see a Gatekeeper warning, run: `sudo xattr -d com.apple.quarantine $(which burn)`

## Quick start

```bash
# See where money goes — by namespace
burn analyze

# Add Prometheus for actual usage data
burn analyze --prometheus http://prometheus:9090

# 7-day average instead of point-in-time
burn analyze --prometheus http://prometheus:9090 --period 7d

# Drill into a namespace
burn analyze --prometheus http://prometheus:9090 --namespace argocd
```

```
NAMESPACE: argocd (4 pods, $46/mo)
──────────────────────────────────
POD                              CPU REQ→USED  MEM REQ→USED   COST/MO
argocd-application-controller-0  500m → 22m    512Mi → 335Mi  $12
argocd-server-5bdc77f5b6-nj...   500m → 1m     512Mi → 34Mi   $12
argocd-dex-server-8fc854b84-...  500m → <1m    512Mi → 20Mi   $12
argocd-redis-7fd8bb554b-zqd...   500m → 2m     512Mi → 5Mi    $12
```

## AI recommendations

```bash
burn analyze --prometheus http://prometheus:9090 --ai
```

burn sends your cluster data to Claude and gets back specific, actionable recommendations:

```
[!!] 1. Convert All 5 Nodes to Spot
   All 5 on-demand t3.large nodes are 71-82% idle, wasting ~$267/month.
   Switching to Spot saves up to $228/month (~65% discount).
   ⚠️ Only for stateless workloads (Deployments with >1 replica).
   $ eksctl create nodegroup --cluster=CLUSTER --spot --nodes=5

[!] 2. Right-size over-provisioned pods
   argocd-dex-server requests 500m CPU but uses 0.012%.
   $ kubectl set resources deployment argocd-dex-server -n argocd \
     --requests=cpu=20m,memory=64Mi

[!] 3. Remove idle debug pods in dev and qa
   Two debug pods costing $9.80/month with near-zero usage.
   $ kubectl delete pod debug-pod -n app-api-dev
```

Requires `ANTHROPIC_API_KEY` environment variable.

## Slack integration

Run burn as a Slack bot:

```bash
burn serve --port 8080 --prometheus http://prometheus:9090 --period 7d
```

Then in Slack:

| Command | Description |
|---------|-------------|
| `/burn` | Namespace cost breakdown |
| `/burn ns argocd` | Pod-level detail for a namespace |
| `/burn ask "compare dev vs prod costs"` | AI-powered cost analysis |

### Slack setup

1. Create a Slack App at https://api.slack.com/apps
2. Add Slash Command: `/burn` → point to your server URL + `/slack`
3. Set `SLACK_SIGNING_SECRET` and `ANTHROPIC_API_KEY` environment variables
4. Expose the server (e.g., ngrok for testing, load balancer for production)

## Deploy to Kubernetes

### Helm (daily reports)

```bash
helm install burn ./charts/burn \
  --set prometheus.url=http://prometheus:9090 \
  --set schedule="0 9 * * 1-5"
```

### CronJob

```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: burn-report
spec:
  schedule: "0 9 * * 1-5"
  jobTemplate:
    spec:
      template:
        spec:
          containers:
          - name: burn
            image: ghcr.io/tanrikuluozlem/burn:latest
            args:
            - analyze
            - --prometheus
            - http://prometheus-server.monitoring:80
            - --period
            - 7d
            - --ai
            - --slack
            env:
            - name: ANTHROPIC_API_KEY
              valueFrom:
                secretKeyRef:
                  name: burn-secrets
                  key: anthropic-api-key
            - name: SLACK_WEBHOOK_URL
              valueFrom:
                secretKeyRef:
                  name: burn-secrets
                  key: slack-webhook-url
          restartPolicy: OnFailure
```

## Configuration

| Variable | Description | Required for |
|----------|-------------|-------------|
| `ANTHROPIC_API_KEY` | Claude API key | `--ai`, `ask`, `serve` |
| `SLACK_WEBHOOK_URL` | Slack webhook URL | `--slack` |
| `SLACK_SIGNING_SECRET` | Slack app signing secret | `serve` |

## How it works

```
kubectl → Nodes & Pods → Pricing API → Cost Report → AI Recommendations
                ↑                                          ↓
           Prometheus                                Slack / CLI
           (optional)
```

Without Prometheus, burn uses pod resource requests to estimate costs. With Prometheus, it shows actual CPU and memory usage — the gap between request and usage is where your money burns.

Pricing data for 600+ AWS instances and 300+ Azure VMs is embedded in the binary and updated weekly via GitHub Actions.

## Development

```bash
make build    # Build binary
make test     # Run tests
make lint     # Run linter
```

## License

Apache 2.0 — See [LICENSE](LICENSE) for details.
