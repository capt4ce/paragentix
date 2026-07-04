# Custom Agent

A deployed, customizable AI agent harness in Go.

PRD: http://168.110.213.104/prd/custom-agent/

## MVP features

- Profile-based configuration
- DeepSeek and other OpenAI-compatible provider adapter
- Codex subscription adapter seam
- Agent loop with normalized tool calls
- Skills loaded from Markdown files
- SQLite persistence for sessions, messages, memories, approvals, MCP servers
- OS tools: file read/create/update/search and shell execution
- Configurable file access policy per profile
- MCP stdio/HTTP adapter seam
- Go Response API: `POST /v1/responses`
- Discord gateway skeleton with approval buttons

## Quick start

```bash
cp config.example.yaml config.yaml
export DEEPSEEK_API_KEY=...
go run ./cmd/custom-agent chat "hello"
go run ./cmd/custom-agent serve --addr :8080
```

## Config

Edit `config.yaml`:

```yaml
model:
  provider: "openai-compatible" # openai-compatible | deepseek | codex-subscription
  model: "deepseek-chat"
  base_url: "https://api.deepseek.com/v1"
  api_key_env: "DEEPSEEK_API_KEY"
```

Discord:

```yaml
discord:
  enabled: true
  token_env: "DISCORD_BOT_TOKEN"
  default_profile: "default"
  allowed_channel_ids: ["..."]
```

## Development

```bash
go test ./...
go build -o bin/custom-agent ./cmd/custom-agent
```

## Kanban board

Hermes board: `custom-agent`

```bash
hermes kanban --board custom-agent list
```
