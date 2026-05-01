# burn

[![CI](https://github.com/tanrikuluozlem/burn/actions/workflows/ci.yml/badge.svg)](https://github.com/tanrikuluozlem/burn/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/tanrikuluozlem/burn)](https://github.com/tanrikuluozlem/burn/releases)
[![Go Report Card](https://goreportcard.com/badge/github.com/tanrikuluozlem/burn)](https://goreportcard.com/report/github.com/tanrikuluozlem/burn)
[![License](https://img.shields.io/github/license/tanrikuluozlem/burn)](LICENSE)

Your Kubernetes cluster is burning money. Find out where.

```
$ burn analyze --prometheus http://prometheus:9090 --period 7d

Kubernetes Cost Report (7d avg)
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
Monthly: $350 | Idle: $117 (33%)
Nodes: 5 | Pods: 77

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

No agent to deploy. No dashboard to maintain. No YAML to configure. Just install and run.

## Why burn

- **Zero setup** — `brew install`, run one command, get answers. No cluster agent, no persistent storage, no config files.
- **Accurate** — Compute, storage, and load balancer costs from cloud provider APIs. Per-resource cost allocation with idle detection.
- **AI-powered** — Ask questions in plain English, get kubectl commands you can copy-paste.
- **Slack-native** — `/burn` for instant cost reports. `/burn ask "..."` for AI analysis. No context switching.
- **Time-aware** — `--period 7d` uses Prometheus history for weekly averages, not just a point-in-time snapshot.

## Install

```bash
# Homebrew
brew install tanrikuluozlem/burn/burn

# Binary
curl -L "https://github.com/tanrikuluozlem/burn/releases/latest/download/burn_$(uname -s | tr '[:upper:]' '[:lower:]')_$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/').tar.gz" | tar xz

# Docker
docker pull ghcr.io/tanrikuluozlem/burn:latest

# Helm
git clone https://github.com/tanrikuluozlem/burn.git
helm install burn ./burn/charts/burn

# Go
go install github.com/tanrikuluozlem/burn/cmd/burn@latest
```

> **macOS:** If you see a Gatekeeper warning, run: `sudo xattr -d com.apple.quarantine $(which burn)`

## Quick start

```bash
# Namespace cost breakdown
burn analyze

# With Prometheus for actual usage data
burn analyze --prometheus http://prometheus:9090

# 7-day average
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
burn analyze --prometheus http://prometheus:9090 --period 7d --ai
```

Burn sends your cluster data to Claude and returns prioritized, actionable recommendations with real node names and ready-to-run commands:

```
RECOMMENDATIONS
───────────────
All 5 nodes are on-demand t3.large instances with 26-41% idle rates,
wasting $117/month. Converting to Spot saves up to $277/month.

[!!] 1. Convert All 5 Nodes to Spot
   All 5 on-demand t3.large nodes have 26-41% idle cost, wasting $117/month.
   Switching to Spot saves up to $277/month (~79% discount).
   ⚠️ Only for stateless workloads (Deployments with >1 replica).
   $ eksctl create nodegroup --cluster=CLUSTER --region=eu-central-1 --spot --nodes=5

[!!] 2. Right-size over-provisioned pods
   argocd-dex-server requests 500m CPU but uses 0.12m (0.0% efficiency), $14/month.
   $ kubectl set resources deployment argocd-dex-server -n argocd \
     --requests=cpu=10m,memory=64Mi

[!] 3. Remove idle debug pods in dev and qa
   Two rds-debug pods costing $5.7/month each with near-zero usage.
   $ kubectl delete pod rds-debug -n app-api-dev

Total potential savings: $277/mo
```

Requires `ANTHROPIC_API_KEY` environment variable.

## Slack integration

Run burn as a Slack bot:

```bash
burn serve --port 8080 --prometheus http://prometheus:9090 --period 7d
```

| Command | What you get |
|---------|-------------|
| `/burn` | Full cost report — nodes, namespaces, idle cost |
| `/burn ns argocd` | Pod-level breakdown for a namespace |
| `/burn ask "why is argocd so expensive?"` | AI analysis with kubectl commands |

Example `/burn ask "compare argocd vs kube-system costs"`:

```
| Metric           | argocd   | kube-system |
|------------------|----------|-------------|
| Monthly Cost     | $55.64   | $41.30      |
| Pod Count        | 4        | 21          |
| CPU Requested    | 2,000m   | 1,420m      |
| CPU Actual Usage | ~30m     | ~52m        |

ArgoCD costs 35% more than kube-system despite having only 4 pods vs 21.

Recommended:
$ kubectl set resources deployment argocd-dex-server -n argocd \
    --requests=cpu=10m,memory=64Mi --limits=cpu=50m,memory=128Mi
```

### Slack setup

1. Create a Slack App at https://api.slack.com/apps
2. Add Slash Command: `/burn` → point to your server URL + `/slack`
3. Set `SLACK_SIGNING_SECRET` and `ANTHROPIC_API_KEY` environment variables
4. Expose the server (e.g., ngrok for testing, load balancer for production)

## How it works

```
Kubernetes API → nodes, pods, PVCs, services, ingresses
Prometheus     → actual CPU & memory usage (optional)
Cloud Pricing  → real VM and storage prices (AWS, Azure, GCP)
         ↓
    Cost Engine → per-namespace breakdown, storage, load balancers, idle detection
         ↓
    CLI / Slack / AI Recommendations
```

Pricing data for 600+ AWS and 300+ Azure instances is embedded and updated weekly via GitHub Actions. Storage and load balancer costs are fetched from cloud APIs at runtime.

## Deploy to Kubernetes

### Helm

```bash
git clone https://github.com/tanrikuluozlem/burn.git
helm install burn ./burn/charts/burn \
  --set prometheus.url=http://prometheus:9090 \
  --set schedule="0 9 * * 1-5"
```

### CronJob (daily Slack reports)

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

## Development

```bash
make build    # Build binary
make test     # Run tests
make lint     # Run linter
```

## License

Apache 2.0 — See [LICENSE](LICENSE) for details.
