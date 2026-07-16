# Paragentix

Self-hosted Go/SQLite board that schedules Codex or Claude Code jobs through tmux.

## Run

```sh
cd frontend && npm install && npm run build
cd .. && go run ./cmd/paragentix
```

Open http://localhost:8080. Data is stored in root `sqlite.db`. Optional `.env` values may be exported by your process manager; `ADDR` controls the listen address. `WORKSPACE_ROOT` limits project directories and `WORKTREE_ROOT` controls server-derived Git worktree paths. Install/authenticate `tmux`, `codex`, and/or `claude` for execution. Settings accepts argv-parsed custom commands such as `codex -m gpt-5.6 --yolo`; commands and prompts never pass through a shell.

## Verify

```sh
go test ./internal/board ./cmd/paragentix
cd frontend && npm test && npm run build
```
