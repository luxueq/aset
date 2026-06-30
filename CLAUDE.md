# AIGateway

> Production-grade AI Agent infrastructure platform built in Go.

## Project Overview

AIGateway is a unified gateway for AI model APIs with Agent runtime capabilities, MCP protocol support, sandbox execution, and multi-agent communication. It serves both internal teams and external customers.

**Core Value:** Let any AI Agent run and collaborate through a unified gateway — securely, efficiently, and observably.

## Architecture Layers

```
L0: Core Gateway     → Proxy + Route + Rate Limit + Cache
L1: Agent Runtime    → SSE Streaming + MCP Server + Backpressure
L2: Agent Infra      → Tool Executor + Sandbox + State + Comm Bus
L3: Governance       → Multi-tenant + RBAC + Guardrails + Audit
```

## Tech Stack

- **Language:** Go
- **HTTP:** `net/http` standard library + custom SSE handlers
- **JSON:** `bytedance/sonic` v2
- **MCP:** Official `modelcontextprotocol/go-sdk`
- **Messaging:** NATS (`nats.go`)
- **Observability:** OpenTelemetry → Prometheus (+ ClickHouse later)
- **Cache:** Redis + semantic cache (embedding similarity)

## Getting Started

```bash
# Build
go build -o aigateway ./cmd/aigateway

# Run
./aigateway --config config.yaml

# Default port: 8080
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer your-api-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"Hello"}]}'
```

## Planning

See `.planning/` for project plans:
- `PROJECT.md` — Project context and decisions
- `ROADMAP.md` — 8-phase execution plan
- `REQUIREMENTS.md` — 56 v1 requirements
- `research/` — Domain research (stack, features, architecture, pitfalls)

## Current Phase

**Phase 1: Core Gateway** — Unified proxy + routing + rate limiting + caching

## Key References

- [MCP Specification](https://modelcontextprotocol.io/specification/draft)
- [MCP Go SDK](https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/mcp)
- [NATS Documentation](https://docs.nats.io/)
- [OpenTelemetry Go](https://opentelemetry.io/docs/languages/go/)
