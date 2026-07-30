package board

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
)

func (a *App) workspaceSettings(w http.ResponseWriter, r *http.Request, workspaceID int64, role string) {
	if r.Method == "PATCH" {
		if role != "owner" {
			fail(w, 403, "owner required")
			return
		}
		var x struct {
			HermesURL       string  `json:"hermes_url"`
			HermesAPIKey    string  `json:"hermes_api_key"`
			HermesModel     string  `json:"hermes_model"`
			TelegramEnabled *bool   `json:"telegram_enabled"`
			TelegramChatID  *string `json:"telegram_chat_id"`
		}
		if decode(r, &x) != nil {
			fail(w, 400, "invalid settings")
			return
		}
		u, e := url.ParseRequestURI(x.HermesURL)
		var savedKey string
		a.DB.QueryRow("SELECT hermes_api_key FROM workspaces WHERE id=?", workspaceID).Scan(&savedKey)
		if e != nil || (u.Scheme != "http" && u.Scheme != "https") || (x.HermesAPIKey == "" && savedKey == "") {
			fail(w, 400, "Hermes URL and API key required")
			return
		}
		a.DB.Exec("UPDATE workspaces SET hermes_url=?,hermes_api_key=CASE WHEN ?='' THEN hermes_api_key ELSE ? END,hermes_model=? WHERE id=?", x.HermesURL, x.HermesAPIKey, x.HermesAPIKey, x.HermesModel, workspaceID)
		if x.TelegramEnabled != nil || x.TelegramChatID != nil {
			var enabled bool
			var chat string
			a.DB.QueryRow(`SELECT telegram_enabled,telegram_chat_id FROM workspaces WHERE id=?`, workspaceID).Scan(&enabled, &chat)
			if x.TelegramEnabled != nil {
				enabled = *x.TelegramEnabled
			}
			if x.TelegramChatID != nil {
				chat = strings.TrimSpace(*x.TelegramChatID)
			}
			if enabled && (chat == "" || a.TelegramBotToken == "") {
				fail(w, 400, "Telegram chat ID and server bot token required")
				return
			}
			a.DB.Exec(`UPDATE workspaces SET telegram_enabled=?,telegram_chat_id=? WHERE id=?`, enabled, chat, workspaceID)
		}
	}
	var hermesURL, hermesModel, key, telegramChatID string
	var telegramEnabled bool
	a.DB.QueryRow("SELECT hermes_url,hermes_model,hermes_api_key,telegram_chat_id,telegram_enabled FROM workspaces WHERE id=?", workspaceID).Scan(&hermesURL, &hermesModel, &key, &telegramChatID, &telegramEnabled)
	jsonOut(w, 200, map[string]any{"hermes_url": hermesURL, "hermes_model": hermesModel, "hermes_api_key": "", "hermes_api_key_set": key != "", "telegram_chat_id": telegramChatID, "telegram_enabled": telegramEnabled, "telegram_available": a.TelegramBotToken != ""})
}

func (a *App) telegramTest(w http.ResponseWriter, r *http.Request, workspaceID int64, role string) {
	if r.Method != "POST" {
		fail(w, 405, "method not allowed")
		return
	}
	if role != "owner" {
		fail(w, 403, "owner required")
		return
	}
	var enabled bool
	var chat string
	if err := a.DB.QueryRow(`SELECT telegram_enabled,telegram_chat_id FROM workspaces WHERE id=?`, workspaceID).Scan(&enabled, &chat); err != nil || !enabled || strings.TrimSpace(chat) == "" || a.TelegramBotToken == "" {
		fail(w, 409, "Telegram notifications are not configured")
		return
	}
	if err := a.sendTelegram(workspaceID, "Paragentix test notification", "Telegram notifications are configured.", 0); err != nil {
		fail(w, 502, err.Error())
		return
	}
	jsonOut(w, 200, map[string]bool{"ok": true})
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
	out := []map[string]any{{"id": "hermes", "name": "Hermes API", "available": true, "reason": "Configured in Settings"}}
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
