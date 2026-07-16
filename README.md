# CLI Agent Job Board

Self-hosted Go/SQLite board that schedules Codex or Claude Code jobs through tmux.

## Run

```sh
cd frontend && npm install && npm run build
cd .. && go run ./cmd/custom-agent
```

Open http://localhost:8080. Data is stored in root `sqlite.db`. Optional `.env` values may be exported by your process manager; `ADDR` controls the listen address. Install/authenticate `tmux`, `codex`, and/or `claude` for execution. Unavailable tools remain visible in Settings.

## Verify

```sh
go test ./internal/board ./cmd/custom-agent
cd frontend && npm test && npm run build
```
