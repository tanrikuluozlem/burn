# burn

[![CI](https://github.com/tanrikuluozlem/burn/actions/workflows/ci.yml/badge.svg)](https://github.com/tanrikuluozlem/burn/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/tanrikuluozlem/burn)](https://github.com/tanrikuluozlem/burn/releases)

Your Kubernetes cluster is burning money. Find out where.

```
NAMESPACES
──────────
NAMESPACE            PODS  CPU REQ→USED  MEM REQ→USED   COST/MO
argocd               4     2.0 → 30m     2.0Gi → 393Mi  $56
amazon-cloudwatch    11    1.6 → 82m     829Mi → 1.3Gi  $44
kube-system          21    1.4 → 52m     1.6Gi → 757Mi  $41
app-api-qa           3     600m → 5m     768Mi → 91Mi   $17
app-api-dev          3     600m → 4m     768Mi → 197Mi  $17
app-api-prod         2     400m → 4m     512Mi → 17Mi   $11
app-web-prod         2     400m → <1m    512Mi → 9Mi    $11
Idle (unallocated)                                     $117
─────────────────────────────────────────────────────────
Total                                                  $350
```

One command. No setup. No dashboard. Just answers.

## What it does

- **Namespace cost breakdown** with request vs actual usage
- **AI recommendations** — rightsizing, spot migration, with copy-paste kubectl commands
- **Slack bot** — `/burn` for cost reports, `/burn ask "..."` for natural language questions
- **Time-based analysis** — `--period 7d` for weekly averages instead of point-in-time snapshots
- **Multi-cloud pricing** — AWS, Azure, GCP (AWS and Azure prices auto-updated weekly)

## Install

```bash
# Homebrew
brew install tanrikuluozlem/burn/burn

# Binary
export VERSION=0.2.7  # check https://github.com/tanrikuluozlem/burn/releases for latest
curl -L "https://github.com/tanrikuluozlem/burn/releases/download/v${VERSION}/burn_${VERSION}_$(uname -s | tr '[:upper:]' '[:lower:]')_$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/').tar.gz" | tar xz

# Docker
docker pull ghcr.io/tanrikuluozlem/burn:latest

# Helm (Kubernetes)
git clone https://github.com/tanrikuluozlem/burn.git
helm install burn ./burn/charts/burn

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
NAMESPACE: argocd (4 pods, $56/mo)
──────────────────────────────────
POD                                CPU REQ→USED  MEM REQ→USED   COST/MO
argocd-application-controller-0    500m → 23m    512Mi → 346Mi  $14
argocd-server-5bdc77f5b6-njxc6     500m → 1m     512Mi → 34Mi   $14
argocd-dex-server-8fc854b84-pxqh5  500m → <1m    512Mi → 20Mi   $14
argocd-redis-7fd8bb554b-zqdcz      500m → 2m     512Mi → 5Mi    $14
```

## AI recommendations

```bash
burn analyze --prometheus http://prometheus:9090 --ai
```

burn sends your cluster data to Claude and gets back specific, actionable recommendations:

```
[!!] 1. Convert All 5 Nodes to Spot
   All 5 on-demand t3.large nodes have 26-41% idle cost, wasting $117/month.
   Switching to Spot saves up to $277/month (~79% discount).
   ⚠️ Only for stateless workloads (Deployments with >1 replica).
   $ eksctl create nodegroup --cluster=CLUSTER --region=eu-central-1 --spot --nodes=5

[!!] 2. Right-size over-provisioned pods
   argocd-dex-server requests 500m CPU but uses 0.12m (0.0% efficiency).
   $ kubectl set resources deployment argocd-dex-server -n argocd \
     --requests=cpu=10m,memory=64Mi

[!] 3. Remove idle debug pods in dev and qa
   Two rds-debug pods costing $5.7/month each with near-zero usage.
   $ kubectl delete pod rds-debug -n app-api-dev
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

Example response for `/burn ask "compare argocd vs kube-system costs"`:

```
ArgoCD vs kube-system Cost Comparison

| Metric           | argocd   | kube-system |
|------------------|----------|-------------|
| Monthly Cost     | $55.64   | $41.30      |
| Pod Count        | 4        | 21          |
| CPU Requested    | 2,000m   | 1,420m      |
| CPU Actual Usage | ~30m     | ~52m        |
| Mem Requested    | 2.0 GiB  | 1.6 GiB     |
| Mem Actual Usage | 393 MiB  | 757 MiB     |

ArgoCD costs 35% more than kube-system despite having only 4 pods vs 21.

ArgoCD is extremely wasteful:
- argocd-dex-server — requests 500m CPU, uses <1m (0.0% efficiency)
- argocd-server — requests 500m CPU, uses ~1m (0.3% efficiency)

Recommended:
$ kubectl set resources deployment argocd-dex-server -n argocd \
    --requests=cpu=10m,memory=64Mi --limits=cpu=50m,memory=128Mi
$ kubectl set resources deployment argocd-server -n argocd \
    --requests=cpu=50m,memory=128Mi --limits=cpu=200m,memory=256Mi
```

### Slack setup

1. Create a Slack App at https://api.slack.com/apps
2. Add Slash Command: `/burn` → point to your server URL + `/slack`
3. Set `SLACK_SIGNING_SECRET` and `ANTHROPIC_API_KEY` environment variables
4. Expose the server (e.g., ngrok for testing, load balancer for production)

## Deploy to Kubernetes

### Helm (daily reports)

```bash
git clone https://github.com/tanrikuluozlem/burn.git
helm install burn ./burn/charts/burn \
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
