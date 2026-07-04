# Custom Agent

A deployed, customizable AI agent harness in Go.

## MVP features

- Profile-based configuration
- OpenAI-compatible provider adapter for DeepSeek/custom APIs
- Codex subscription adapter seam
- Agent loop with normalized tool calls
- Skills loaded from Markdown frontmatter
- SQLite persistence for sessions, messages, memories, approvals, MCP servers
- OS tools: file read/create/update/search and shell execution
- MCP stdio/HTTP adapter seam
- Go Response API
- Discord gateway skeleton with approval buttons

## Quick start

```bash
cp config.example.yaml config.yaml
go run ./cmd/custom-agent chat "hello"
go run ./cmd/custom-agent serve --addr :8080
```

## Kanban board

Hermes board: `custom-agent`

```bash
hermes kanban --board custom-agent list
```
