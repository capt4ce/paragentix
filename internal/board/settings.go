package board

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
)

func (a *App) settings(w http.ResponseWriter, r *http.Request) {
	if r.Method == "PATCH" {
		var x struct {
			DefaultCLI   string            `json:"default_cli"`
			Commands     map[string]string `json:"commands"`
			HermesURL    string            `json:"hermes_url"`
			HermesAPIKey string            `json:"hermes_api_key"`
			HermesModel  string            `json:"hermes_model"`
		}
		if decode(r, &x) != nil || (x.DefaultCLI != "" && x.DefaultCLI != "codex" && x.DefaultCLI != "claude" && x.DefaultCLI != "hermes") {
			fail(w, 400, "invalid CLI")
			return
		}
		if x.DefaultCLI == "hermes" {
			u, e := url.ParseRequestURI(x.HermesURL)
			var savedKey string
			a.DB.QueryRow("SELECT hermes_api_key FROM user_settings WHERE user_id=?", uid(r)).Scan(&savedKey)
			if e != nil || (u.Scheme != "http" && u.Scheme != "https") || (x.HermesAPIKey == "" && savedKey == "") {
				fail(w, 400, "Hermes URL and API key required")
				return
			}
		}
		if e := storeCommands(a, uid(r), x.Commands); e != nil {
			fail(w, 400, e.Error())
			return
		}
		if x.DefaultCLI != "" {
			a.DB.Exec("UPDATE user_settings SET default_cli=?,hermes_url=?,hermes_api_key=CASE WHEN ?='' THEN hermes_api_key ELSE ? END,hermes_model=?,updated_at=CURRENT_TIMESTAMP WHERE user_id=?", x.DefaultCLI, x.HermesURL, x.HermesAPIKey, x.HermesAPIKey, x.HermesModel, uid(r))
		}
	}
	var cli, root, hermesURL, hermesModel, key string
	a.DB.QueryRow("SELECT default_cli,workspace_root,hermes_url,hermes_model,hermes_api_key FROM user_settings WHERE user_id=?", uid(r)).Scan(&cli, &root, &hermesURL, &hermesModel, &key)
	commands, _ := commandSettingsJSON(a, uid(r))
	jsonOut(w, 200, map[string]any{"default_cli": cli, "workspace_root": root, "commands": commands, "hermes_url": hermesURL, "hermes_model": hermesModel, "hermes_api_key": "", "hermes_api_key_set": key != ""})
}
func available(name string) bool { _, e := exec.LookPath(name); return e == nil }
func (a *App) tools(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		var x struct {
			Name string
			Argv []string
		}
		if decode(r, &x) != nil || strings.TrimSpace(x.Name) == "" || len(x.Argv) == 0 || strings.TrimSpace(x.Argv[0]) == "" {
			fail(w, 400, "name and argv required")
			return
		}
		b, _ := json.Marshal(x.Argv)
		res, e := a.DB.Exec("INSERT INTO custom_cli_tools(user_id,name,argv_json) VALUES(?,?,?)", uid(r), strings.TrimSpace(x.Name), b)
		if e != nil {
			fail(w, 409, "tool unavailable")
			return
		}
		id, _ := res.LastInsertId()
		jsonOut(w, 201, map[string]any{"id": id})
		return
	}
	out := []map[string]any{{"id": "codex", "name": "Codex", "available": available("codex"), "reason": reason("codex")}, {"id": "claude", "name": "Claude Code", "available": available("claude"), "reason": reason("claude")}}
	rows, _ := a.DB.Query("SELECT id,name,argv_json FROM custom_cli_tools WHERE user_id=?", uid(r))
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name, s string
		rows.Scan(&id, &name, &s)
		var argv []string
		json.Unmarshal([]byte(s), &argv)
		out = append(out, map[string]any{"id": id, "name": name, "argv": argv, "available": available(argv[0]), "reason": reason(argv[0])})
	}
	jsonOut(w, 200, out)
}
func reason(s string) string {
	if available(s) {
		return "Available"
	}
	return "Not installed"
}
func commandSettingsJSON(a *App, user int64) (map[string]string, error) {
	out := map[string]string{"codex": "codex", "claude": "claude"}
	rows, e := a.DB.Query(`SELECT name,command FROM custom_cli_tools WHERE user_id=? AND name IN ('codex','claude')`, user)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var n, c string
		rows.Scan(&n, &c)
		out[n] = c
	}
	return out, nil
}
func storeCommands(a *App, user int64, cmds map[string]string) error {
	for _, tool := range []string{"codex", "claude"} {
		c, ok := cmds[tool]
		if !ok {
			continue
		}
		argv, e := parseCommand(c)
		if e != nil {
			return fmt.Errorf("%s command: %w", tool, e)
		}
		b, _ := json.Marshal(argv)
		_, e = a.DB.Exec(`INSERT INTO custom_cli_tools(user_id,name,command,argv_json) VALUES(?,?,?,?) ON CONFLICT(user_id,name) DO UPDATE SET command=excluded.command,argv_json=excluded.argv_json`, user, tool, c, b)
		if e != nil {
			return e
		}
	}
	return nil
}
