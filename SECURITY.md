# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| Latest release | Yes |
| Older releases | No |

## Reporting a Vulnerability

Please report security vulnerabilities privately. Do not open a public GitHub issue.

**Email:** me@ozlem.dev

Include:
- Description of the issue
- Steps to reproduce
- Affected versions
- Suggested fix if available

## Response Timeline

I will respond as quickly as possible. As a solo maintainer, I cannot guarantee fixed response times, but I treat security reports as highest priority.

## Security Model

Burn is designed as a read-only Kubernetes and cloud-cost analysis tool.

It does not create, modify, or delete Kubernetes resources.

It does not access Kubernetes Secrets, ConfigMaps, or ServiceAccounts.

Cloud credentials are handled through the standard AWS and Azure SDK credential chains. Burn does not persist cloud credentials to disk.

Dependencies are monitored with GitHub Dependabot for known security vulnerabilities.

## AI and External Integrations

Burn supports multiple optional integration modes. Core CLI commands (`burn analyze`, `burn reconcile`, `burn analyze --spot`) operate without AI and do not send data to any external AI provider.

### CLI AI Analysis

AI-enhanced analysis is enabled explicitly with the `--ai` flag and requires `ANTHROPIC_API_KEY`. When enabled, burn sends cost data to the Anthropic API including node names, pod names, namespace names, instance types, regions, and cost metrics. With `reconcile --ai`, billing reconciliation details including disk volume IDs, load balancer names, and coverage gap data are also sent.

It does not send AWS account IDs, Azure subscription IDs, API keys, or credentials.

### MCP Server

`burn mcp` exposes burn's analysis tools (analyze, spot_readiness, reconcile) to MCP-compatible clients. Burn itself does not call Anthropic in MCP mode. The MCP client (Claude Code, Cursor, or other host) is responsible for the LLM interaction. Operators should review the MCP client's model, privacy, and data-retention settings. Burn's MCP tools are read-only and do not modify cluster or cloud resources.

### Slack Integration

`burn serve` starts an HTTP server for Slack slash commands. It requires `SLACK_SIGNING_SECRET` for request verification and `ANTHROPIC_API_KEY` for the `/burn ask` command.

Only `/burn ask` sends data to Anthropic. Other Slack commands do not send data to any external AI provider.

The server verifies Slack request signatures (HMAC-SHA256) and rejects stale requests older than 5 minutes. It does not provide TLS directly. Place it behind a TLS-terminating reverse proxy or load balancer. Default port: 8080 (configurable via --port).

## Responsible Disclosure

Reported vulnerabilities are handled using a coordinated disclosure process.

When feasible, I aim to prepare and release a fix before public disclosure.

