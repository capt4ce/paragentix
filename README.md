# Paragentix

Self-hosted Go/SQLite board that schedules Hermes API or custom CLI jobs.

## Run

```sh
cd frontend && npm install && npm run build
cd .. && go run ./cmd/paragentix
```

The Go process serves the API on http://localhost:8080. Serve `frontend/dist` from nginx (or another static web server), use an SPA fallback to `index.html`, and proxy `/api/` to the Go process. Data is stored in root `sqlite.db`. Optional `.env` values may be exported by your process manager; `ADDR` controls the listen address. `WORKSPACE_ROOT` limits project directories and `WORKTREE_ROOT` controls server-derived Git worktree paths. `TELEGRAM_BOT_TOKEN` enables the server-managed Telegram channel, and `PARAGENTIX_BASE_URL` supplies links in Telegram messages. Configure each workspace's Telegram chat ID and Hermes connection in Settings. Custom commands and prompts never pass through a shell.

## Verify

```sh
go test ./internal/board ./cmd/paragentix
cd frontend && npm test && npm run build
```
