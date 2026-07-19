package board

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func req(t *testing.T, h http.Handler, c *http.Cookie, method, path, body string) (*httptest.ResponseRecorder, *http.Cookie) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if c != nil {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out *http.Cookie
	for _, x := range w.Result().Cookies() {
		if x.Name == "session" {
			out = x
		}
	}
	return w, out
}
func TestV2WorkspaceOwnershipAliasesAndCustomTools(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0755); err != nil {
		t.Fatal(err)
	}
	a, err := Open(filepath.Join(t.TempDir(), "db"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := a.Handler()
	_, owner := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"owner@example.com","password":"password1"}`)
	_, other := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"other@example.com","password":"password2"}`)

	w, _ := req(t, h, owner, "GET", "/api/workspaces", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"role":"owner"`) || strings.Contains(w.Body.String(), "projectDirectory") {
		t.Fatalf("workspaces: %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, owner, "POST", "/api/projects", `{"name":"App","directory":"project"}`)
	if w.Code != 201 {
		t.Fatalf("project: %d %s", w.Code, w.Body.String())
	}
	var projectOut map[string]any
	json.Unmarshal(w.Body.Bytes(), &projectOut)
	projectID := int64(projectOut["id"].(float64))
	w, _ = req(t, h, owner, "POST", "/api/projects", `{"name":"Escape","directory":"../escape"}`)
	if w.Code != 400 {
		t.Fatalf("directory escape accepted: %d", w.Code)
	}
	w, _ = req(t, h, other, "GET", "/api/projects/"+itoa(projectID), "")
	if w.Code != 404 {
		t.Fatalf("cross-user project access=%d", w.Code)
	}
	w, _ = req(t, h, owner, "POST", "/api/cli-tools", `{"name":"Shell","argv":["sh","-c","printf ok"]}`)
	if w.Code != 201 {
		t.Fatalf("tool: %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, owner, "GET", "/api/boards", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"Default Board"`) || !strings.Contains(w.Body.String(), `"workspaceName":"Default"`) {
		t.Fatalf("signup default board: %d %s", w.Code, w.Body.String())
	}
}

func TestHermesSettingsRequireURLAndAPIKey(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := a.Handler()
	_, cookie := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"hermes@example.com","password":"password1"}`)
	w, _ := req(t, h, cookie, "PATCH", "/api/settings", `{"hermes_url":"","hermes_api_key":""}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing config accepted: %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, cookie, "PATCH", "/api/settings", `{"hermes_url":"http://127.0.0.1:9999","hermes_api_key":"secret","hermes_model":"hermes-agent"}`)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "default_cli") || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("settings: %d %s", w.Code, w.Body.String())
	}
}

func TestRunHermesAcceptsV1BaseURL(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("X-Hermes-Session-Id"); got != "session-123" {
			t.Errorf("session header=%q", got)
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
	}))
	defer ts.Close()
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.DB.Exec("INSERT INTO users(email,password_hash) VALUES('hermes-run@example.com','x')")
	a.DB.Exec("INSERT INTO user_settings(user_id,workspace_root,hermes_url,hermes_api_key,hermes_model) VALUES((SELECT id FROM users WHERE email='hermes-run@example.com'),?,?,?,?)", t.TempDir(), ts.URL+"/v1/", "secret", "hermes-agent")
	var userID int64
	a.DB.QueryRow("SELECT id FROM users WHERE email='hermes-run@example.com'").Scan(&userID)
	got, err := a.runHermesSession(context.Background(), userID, "Reply OK", "session-123")
	if err != nil || got != "OK" {
		t.Fatalf("runHermes: got %q, err %v", got, err)
	}
}

func TestRetryReusesLatestHermesSessionAndRun(t *testing.T) {
	requestSeen := make(chan struct{}, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Hermes-Session-Id"); got != "latest-session" {
			t.Errorf("session header=%q, want latest-session", got)
		}
		var body struct {
			Messages []struct {
				Role, Content string
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Messages) != 1 || body.Messages[0].Role != "user" || body.Messages[0].Content != "retry" {
			t.Errorf("retry request body=%+v err=%v", body, err)
		}
		requestSeen <- struct{}{}
		w.Write([]byte(`{"choices":[{"message":{"content":"retried"}}]}`))
	}))
	defer ts.Close()

	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := a.Handler()
	_, cookie := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"retry@example.com","password":"password1"}`)
	var user, lane int64
	a.DB.QueryRow("SELECT id FROM users WHERE email='retry@example.com'").Scan(&user)
	a.DB.QueryRow("SELECT id FROM lanes WHERE user_id=?", user).Scan(&lane)
	a.DB.Exec("UPDATE lanes SET paused=1 WHERE id=?", lane)
	a.DB.Exec("UPDATE user_settings SET hermes_url=?,hermes_api_key='secret' WHERE user_id=?", ts.URL, user)
	res, err := a.DB.Exec("INSERT INTO jobs(user_id,lane_id,task,state,position,attempt_count,finished_at) VALUES(?,?,'work','done',0,1,CURRENT_TIMESTAMP)", user, lane)
	if err != nil {
		t.Fatal(err)
	}
	job, _ := res.LastInsertId()
	a.DB.Exec("INSERT INTO job_runs(job_id,attempt,tmux_session,status,ended_at) VALUES(?,1,'hermes-api:older-session','done',CURRENT_TIMESTAMP)", job)
	res, err = a.DB.Exec("INSERT INTO job_runs(job_id,attempt,tmux_session,status,ended_at) VALUES(?,1,'hermes-api:latest-session','done',CURRENT_TIMESTAMP)", job)
	if err != nil {
		t.Fatal(err)
	}
	run, _ := res.LastInsertId()

	w, _ := req(t, h, cookie, "POST", "/api/jobs/"+itoa(job)+"/retry", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("retry=%d %s", w.Code, w.Body.String())
	}
	select {
	case <-requestSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("Hermes retry request not received")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		var state string
		a.DB.QueryRow("SELECT state FROM jobs WHERE id=?", job).Scan(&state)
		if state == "done" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job state=%q, want done", state)
		}
		time.Sleep(10 * time.Millisecond)
	}
	var attempts, runs int
	a.DB.QueryRow("SELECT attempt_count FROM jobs WHERE id=?", job).Scan(&attempts)
	a.DB.QueryRow("SELECT count(*) FROM job_runs WHERE job_id=?", job).Scan(&runs)
	if attempts != 1 || runs != 2 {
		t.Fatalf("attempts=%d runs=%d, want unchanged 1 and 2", attempts, runs)
	}
	var status, summary string
	a.DB.QueryRow("SELECT status,result_summary FROM job_runs WHERE id=?", run).Scan(&status, &summary)
	if status != "done" || summary != "retried" {
		t.Fatalf("latest run status=%q summary=%q", status, summary)
	}
	var retryEvents int
	a.DB.QueryRow("SELECT count(*) FROM job_events WHERE job_run_id=? AND kind='retry'", run).Scan(&retryEvents)
	if retryEvents != 1 {
		t.Fatalf("retry events on reused run=%d", retryEvents)
	}
}

func TestReconcileHermesRestartBlockFromCurrentSession(t *testing.T) {
	for _, tc := range []struct {
		name, messages, wantState, wantRun string
	}{
		{"completed", `{"object":"list","data":[{"role":"user","content":"work"},{"role":"assistant","content":"finished remotely"}]}`, "done", "done"},
		{"active", `{"object":"list","data":[{"role":"user","content":"work"}]}`, "in_progress", "running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer secret" {
					t.Errorf("authorization=%q", r.Header.Get("Authorization"))
				}
				switch r.URL.Path {
				case "/api/sessions/hermes-session":
					w.Write([]byte(`{"object":"hermes.session","session":{"id":"hermes-session","ended_at":null,"end_reason":null}}`))
				case "/api/sessions/hermes-session/messages":
					w.Write([]byte(tc.messages))
				default:
					http.NotFound(w, r)
				}
			}))
			defer ts.Close()
			a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			req(t, a.Handler(), nil, "POST", "/api/auth/signup", `{"email":"recover@example.com","password":"password1"}`)
			var user, lane int64
			a.DB.QueryRow("SELECT id FROM users WHERE email='recover@example.com'").Scan(&user)
			a.DB.QueryRow("SELECT id FROM lanes WHERE user_id=?", user).Scan(&lane)
			a.DB.Exec("UPDATE user_settings SET hermes_url=?,hermes_api_key='secret' WHERE user_id=?", ts.URL+"/v1", user)
			res, err := a.DB.Exec("INSERT INTO jobs(user_id,lane_id,task,state,warning,position,attempt_count) VALUES(?,?,'work','blocked','Execution session missing after server restart',0,1)", user, lane)
			if err != nil {
				t.Fatal(err)
			}
			job, _ := res.LastInsertId()
			res, _ = a.DB.Exec("INSERT INTO job_runs(job_id,attempt,tmux_session,status,result_summary,ended_at) VALUES(?,1,'hermes-api:hermes-session','blocked','Execution session missing after server restart',CURRENT_TIMESTAMP)", job)
			run, _ := res.LastInsertId()
			a.DB.Exec("INSERT INTO job_events(job_run_id,sequence,kind,content) VALUES(?,1,'error','Execution session missing after server restart')", run)

			a.reconcile()

			var state, warning, runStatus string
			a.DB.QueryRow("SELECT state,warning FROM jobs WHERE id=?", job).Scan(&state, &warning)
			a.DB.QueryRow("SELECT status FROM job_runs WHERE id=?", run).Scan(&runStatus)
			if state != tc.wantState || runStatus != tc.wantRun || warning != "" {
				t.Fatalf("state=%q run=%q warning=%q", state, runStatus, warning)
			}
			var original int
			a.DB.QueryRow("SELECT count(*) FROM job_events WHERE job_run_id=? AND kind='error'", run).Scan(&original)
			if original != 1 {
				t.Fatalf("original timeline events=%d", original)
			}
			if tc.wantState == "done" {
				var output int
				a.DB.QueryRow("SELECT count(*) FROM job_events WHERE job_run_id=? AND kind='reply' AND content='finished remotely'", run).Scan(&output)
				if output != 1 {
					t.Fatalf("recovered output events=%d", output)
				}
			}
		})
	}
}

func TestInitialHermesPromptIncludesColumnProject(t *testing.T) {
	prompt := initialHermesPrompt("Paragentix", "/srv/projects/paragentix", "Fix the scheduler", "Tests pass")
	want := "Unless otherwise specified, this conversation concerns the project Paragentix, located at /srv/projects/paragentix. Use this project as the default when creating or modifying jobs. Use the direct terminal tool with /srv/projects/paragentix as the workdir for shell commands; do not wrap terminal in execute_code. Delegated shell work must request terminal explicitly. If an indirect terminal attempt fails, retry with the direct terminal tool before claiming terminal is unavailable.\n\nFix the scheduler\n\nDone definition:\nTests pass"
	if prompt != want {
		t.Fatalf("prompt = %q, want %q", prompt, want)
	}
}

func TestSettingsRequireAuthentication(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	w, _ := req(t, a.Handler(), nil, "GET", "/api/settings", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("logged-out settings access=%d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestNotificationsArePaginatedAndOwnerScoped(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := a.Handler()
	_, owner := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"notify@example.com","password":"password1"}`)
	req(t, h, nil, "POST", "/api/auth/signup", `{"email":"other-notify@example.com","password":"password1"}`)
	var ownerID, otherID int64
	a.DB.QueryRow("SELECT id FROM users WHERE email='notify@example.com'").Scan(&ownerID)
	a.DB.QueryRow("SELECT id FROM users WHERE email='other-notify@example.com'").Scan(&otherID)
	for i := 0; i < 12; i++ {
		a.DB.Exec("INSERT INTO notifications(user_id,job_id,kind,title) VALUES(?,NULL,'done',?)", ownerID, fmt.Sprintf("Job %02d", i))
	}
	a.DB.Exec("INSERT INTO notifications(user_id,job_id,kind,title) VALUES(?,NULL,'error','Private')", otherID)
	w, _ := req(t, h, owner, "GET", "/api/notifications?limit=10", "")
	var page struct {
		Notifications []map[string]any `json:"notifications"`
		HasMore       bool             `json:"has_more"`
	}
	if json.Unmarshal(w.Body.Bytes(), &page) != nil || w.Code != 200 || len(page.Notifications) != 10 || !page.HasMore || strings.Contains(w.Body.String(), "Private") {
		t.Fatalf("page: %d %s", w.Code, w.Body.String())
	}
	first := int64(page.Notifications[0]["id"].(float64))
	w, _ = req(t, h, owner, "PATCH", "/api/notifications/"+itoa(first), `{"read":true}`)
	if w.Code != 200 {
		t.Fatalf("read: %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, owner, "POST", "/api/notifications/mark-read", `{}`)
	if w.Code != 200 {
		t.Fatalf("mark read: %d %s", w.Code, w.Body.String())
	}
	var unread int
	a.DB.QueryRow("SELECT count(*) FROM notifications WHERE user_id=? AND read=0", ownerID).Scan(&unread)
	if unread != 0 {
		t.Fatalf("owner has %d unread notifications after marking all read", unread)
	}
	a.DB.QueryRow("SELECT count(*) FROM notifications WHERE user_id=? AND read=0", otherID).Scan(&unread)
	if unread != 1 {
		t.Fatalf("other user has %d unread notifications, want 1", unread)
	}
}

func TestObsoleteColumnMigration(t *testing.T) {
	var err error
	db := filepath.Join(t.TempDir(), "existing.db")
	a, err := Open(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec(`INSERT INTO users(email,password_hash) VALUES('legacy@example.com',x'00')`); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec(`INSERT INTO user_settings(user_id,workspace_root) VALUES((SELECT id FROM users WHERE email='legacy@example.com'),'')`); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec(`INSERT INTO lanes(user_id,name,position,paused) VALUES((SELECT id FROM users WHERE email='legacy@example.com'),'legacy lane',0,1);
INSERT INTO jobs(user_id,lane_id,task,position,pending_comment) VALUES((SELECT id FROM users WHERE email='legacy@example.com'),(SELECT id FROM lanes WHERE name='legacy lane'),'preserve me',0,'follow up')`); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec(`ALTER TABLE user_settings ADD COLUMN default_cli TEXT NOT NULL DEFAULT 'codex';
ALTER TABLE jobs ADD COLUMN cli_tool TEXT NOT NULL DEFAULT 'codex';
ALTER TABLE custom_cli_tools ADD COLUMN command TEXT NOT NULL DEFAULT '';
INSERT INTO custom_cli_tools(user_id,name,command,argv_json) VALUES((SELECT id FROM users WHERE email='legacy@example.com'),'local-agent','local-agent --safe','["local-agent","--safe"]');`); err != nil {
		t.Fatal(err)
	}
	a.Close()
	a, err = Open(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	var n int
	if err = a.DB.QueryRow(`SELECT count(*) FROM users WHERE email='legacy@example.com'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("legacy data lost: %d %v", n, err)
	}
	for _, table := range []string{"workspaces", "boards", "columns", "custom_cli_tools"} {
		if err = a.DB.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("missing %s: %v", table, err)
		}
	}
	if err = a.DB.QueryRow(`SELECT count(*) FROM pragma_table_info('custom_cli_tools') WHERE name='command'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("obsolete command column remains: %d %v", n, err)
	}
	for table, column := range map[string]string{"user_settings": "default_cli", "jobs": "cli_tool"} {
		if err = a.DB.QueryRow(`SELECT count(*) FROM pragma_table_info(?) WHERE name=?`, table, column).Scan(&n); err != nil || n != 0 {
			t.Fatalf("obsolete %s.%s remains: %d %v", table, column, n, err)
		}
	}
	var argv string
	if err = a.DB.QueryRow(`SELECT argv_json FROM custom_cli_tools WHERE name='local-agent'`).Scan(&argv); err != nil || argv != `["local-agent","--safe"]` {
		t.Fatalf("custom tool lost: %q %v", argv, err)
	}
	var task, comment string
	if err = a.DB.QueryRow(`SELECT task,pending_comment FROM jobs WHERE task='preserve me'`).Scan(&task, &comment); err != nil || task != "preserve me" || comment != "follow up" {
		t.Fatalf("job data lost: task=%q comment=%q err=%v", task, comment, err)
	}
	if err = a.migrate(); err != nil {
		t.Fatalf("idempotent rerun: %v", err)
	}
}

func TestFreshAccountLanesReturnEmptyJobsArray(t *testing.T) {
	a, e := Open(t.TempDir()+"/db", t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	h := a.Handler()
	w, cookie := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"fresh@example.com","password":"password1"}`)
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	w, _ = req(t, h, cookie, "GET", "/api/lanes", "")
	if !strings.Contains(w.Body.String(), `"jobs":[]`) {
		t.Fatalf("empty jobs must be an array: %s", w.Body.String())
	}
}

func TestV2ColumnUsesMappedLane(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "new")
	if err := os.Mkdir(workspace, 0755); err != nil {
		t.Fatal(err)
	}
	a, err := Open(filepath.Join(t.TempDir(), "db"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := a.Handler()
	_, cookie := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"column@example.com","password":"password1"}`)
	w, _ := req(t, h, cookie, "POST", "/api/workspaces", `{"name":"New","projectDirectory":"`+workspace+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("workspace: %d %s", w.Code, w.Body.String())
	}
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	workspaceID := int64(out["id"].(float64))
	w, _ = req(t, h, cookie, "POST", "/api/boards", `{"name":"Board","workspaceId":`+itoa(workspaceID)+`}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("board: %d %s", w.Code, w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	boardID := int64(out["id"].(float64))
	w, _ = req(t, h, cookie, "POST", "/api/workspaces/"+itoa(workspaceID)+"/projects", `{"name":"App","directory":"`+workspace+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("project: %d %s", w.Code, w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	projectID := int64(out["id"].(float64))
	w, _ = req(t, h, cookie, "POST", "/api/boards/"+itoa(boardID)+"/columns", `{"projectId":`+itoa(projectID)+`,"worktreeEnabled":false}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("column: %d %s", w.Code, w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	columnID := int64(out["id"].(float64))
	var laneID int64
	if err := a.DB.QueryRow(`SELECT lane_id FROM columns WHERE id=?`, columnID).Scan(&laneID); err != nil || laneID == columnID {
		t.Fatalf("column/lane mapping: column=%d lane=%d err=%v", columnID, laneID, err)
	}
	w, _ = req(t, h, cookie, "POST", "/api/columns/"+itoa(columnID)+"/jobs", `{"task":"mapped","done_definition":"done"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("job: %d %s", w.Code, w.Body.String())
	}
	var n int
	if err := a.DB.QueryRow(`SELECT count(*) FROM jobs WHERE lane_id=?`, laneID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("job lane: count=%d err=%v", n, err)
	}
	w, _ = req(t, h, cookie, "GET", "/api/boards/"+itoa(boardID)+"/columns", "")
	var columns []struct {
		Jobs []Job `json:"jobs"`
	}
	if json.Unmarshal(w.Body.Bytes(), &columns) != nil || len(columns) != 1 || len(columns[0].Jobs) != 1 || columns[0].Jobs[0].Task != "mapped" || columns[0].Jobs[0].Creator != "column@example.com" {
		t.Fatalf("column jobs: %d %s", w.Code, w.Body.String())
	}
}

func TestAuthIsolationAndStateValidation(t *testing.T) {
	a, e := Open(t.TempDir()+"/db", t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	h := a.Handler()
	w, c1 := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"a@example.com","password":"password1"}`)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	_, c2 := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"b@example.com","password":"password2"}`)
	w, _ = req(t, h, c1, "GET", "/api/lanes", "")
	var lanes []Lane
	json.Unmarshal(w.Body.Bytes(), &lanes)
	// Keep the scheduler from racing this API state-validation test.
	a.DB.Exec("UPDATE lanes SET paused=1 WHERE id=?", lanes[0].ID)
	w, _ = req(t, h, c1, "POST", "/api/lanes/"+itoa(lanes[0].ID)+"/jobs", `{"task":"hello","done_definition":"works"}`)
	var made map[string]any
	json.Unmarshal(w.Body.Bytes(), &made)
	id := int64(made["id"].(float64))
	w, _ = req(t, h, c2, "GET", "/api/jobs/"+itoa(id), "")
	if w.Code != 404 {
		t.Fatalf("cross-user access=%d", w.Code)
	}
	a.DB.Exec("UPDATE jobs SET state='done',finished_at=CURRENT_TIMESTAMP WHERE id=?", id)
	w, _ = req(t, h, c1, "PATCH", "/api/jobs/"+itoa(id), `{"done_definition":"changed"}`)
	if w.Code != 409 {
		t.Fatalf("done edit=%d", w.Code)
	}
	w, _ = req(t, h, c1, "POST", "/api/jobs/"+itoa(id)+"/retry", `{}`)
	if w.Code != 409 {
		t.Fatalf("retry without Hermes session=%d %s", w.Code, w.Body.String())
	}
	var state string
	var finished any
	if e := a.DB.QueryRow("SELECT state,finished_at FROM jobs WHERE id=?", id).Scan(&state, &finished); e != nil || state != "done" || finished == nil {
		t.Fatalf("rejected retry changed job state=%q finished=%v err=%v", state, finished, e)
	}
	var retryEvents int
	a.DB.QueryRow(`SELECT count(*) FROM job_events e JOIN job_runs r ON r.id=e.job_run_id WHERE r.job_id=? AND e.kind='retry'`, id).Scan(&retryEvents)
	if retryEvents != 0 {
		t.Fatalf("rejected retry timeline events=%d", retryEvents)
	}
	w, _ = req(t, h, c2, "DELETE", "/api/jobs/"+itoa(id), "")
	if w.Code != 404 {
		t.Fatalf("cross-user archive=%d", w.Code)
	}
	res, e := a.DB.Exec("INSERT INTO job_runs(job_id,attempt,tmux_session,status) VALUES(?,1,'archived-test','done')", id)
	if e != nil {
		t.Fatal(e)
	}
	runID, _ := res.LastInsertId()
	if _, e = a.DB.Exec("INSERT INTO job_events(job_run_id,sequence,kind,content) VALUES(?,1,'output','done')", runID); e != nil {
		t.Fatal(e)
	}
	w, _ = req(t, h, c1, "DELETE", "/api/jobs/"+itoa(id), "")
	if w.Code != 204 {
		t.Fatalf("archive done=%d %s", w.Code, w.Body.String())
	}
	var count int
	a.DB.QueryRow("SELECT COUNT(*) FROM jobs WHERE id=? AND archived=1", id).Scan(&count)
	if count != 1 {
		t.Fatal("archived job was not retained")
	}
	a.DB.QueryRow("SELECT COUNT(*) FROM job_runs WHERE id=?", runID).Scan(&count)
	if count != 1 {
		t.Fatal("archived job run was not retained")
	}
	w, _ = req(t, h, c1, "GET", "/api/jobs/"+itoa(id), "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"kind":"archive"`) || !strings.Contains(w.Body.String(), `"content":"done"`) {
		t.Fatalf("archived detail history: %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, c2, "GET", "/api/jobs/"+itoa(id), "")
	if w.Code != 404 {
		t.Fatalf("cross-user archived detail=%d", w.Code)
	}
}
func TestJobCommentSendsToActiveSessionAndRecordsEvent(t *testing.T) {
	a, e := Open(t.TempDir()+"/db", t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	h := a.Handler()
	_, c := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"comment@example.com","password":"password1"}`)
	w, _ := req(t, h, c, "GET", "/api/lanes", "")
	var lanes []Lane
	json.Unmarshal(w.Body.Bytes(), &lanes)
	a.DB.Exec("UPDATE lanes SET paused=1 WHERE id=?", lanes[0].ID)
	w, _ = req(t, h, c, "POST", "/api/lanes/"+itoa(lanes[0].ID)+"/jobs", `{"task":"hello","done_definition":"works"}`)
	var made map[string]any
	json.Unmarshal(w.Body.Bytes(), &made)
	id := int64(made["id"].(float64))
	a.DB.Exec("UPDATE jobs SET state='in_progress' WHERE id=?", id)
	r, _ := a.DB.Exec("INSERT INTO job_runs(job_id,attempt,tmux_session,status) VALUES(?,1,?,'running')", id, "agent-job-"+itoa(id))
	runID, _ := r.LastInsertId()
	session := "agent-job-" + itoa(id)
	if e := exec.Command("tmux", "new-session", "-d", "-s", session).Run(); e != nil {
		t.Skip("tmux unavailable:", e)
	}
	defer exec.Command("tmux", "kill-session", "-t", session).Run()

	w, _ = req(t, h, c, "POST", "/api/jobs/"+itoa(id)+"/comment", `{"comment":"keep the API shape"}`)
	if w.Code != 200 {
		t.Fatalf("comment: %d %s", w.Code, w.Body.String())
	}
	var kind, content string
	if e := a.DB.QueryRow("SELECT kind,content FROM job_events WHERE job_run_id=?", runID).Scan(&kind, &content); e != nil || kind != "comment" || content != "keep the API shape" {
		t.Fatalf("event: kind=%q content=%q err=%v", kind, content, e)
	}

	w, _ = req(t, h, c, "POST", "/api/jobs/"+itoa(id)+"/comment", `{"comment":"   "}`)
	if w.Code != 400 {
		t.Fatalf("blank comment=%d", w.Code)
	}
	exec.Command("tmux", "kill-session", "-t", session).Run()
	a.DB.Exec("UPDATE jobs SET state='done' WHERE id=?", id)
	w, _ = req(t, h, c, "POST", "/api/jobs/"+itoa(id)+"/comment", `{"comment":"late"}`)
	if w.Code != 200 {
		t.Fatalf("done comment=%d", w.Code)
	}
	var state string
	a.DB.QueryRow("SELECT state FROM jobs WHERE id=?", id).Scan(&state)
	if state != "todo" && state != "in_progress" {
		t.Fatalf("done comment state=%q", state)
	}
}

func TestCommentOnDoneJobRequeuesAtEndOfTodoOrder(t *testing.T) {
	a, e := Open(t.TempDir()+"/db", t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	h := a.Handler()
	_, c := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"requeue-comment@example.com","password":"password1"}`)
	w, _ := req(t, h, c, "GET", "/api/lanes", "")
	var lanes []Lane
	json.Unmarshal(w.Body.Bytes(), &lanes)
	laneID := lanes[0].ID
	a.DB.Exec("UPDATE lanes SET paused=1 WHERE id=?", laneID)

	ids := make([]int64, 3)
	for i, task := range []string{"completed", "todo one", "todo two"} {
		w, _ = req(t, h, c, "POST", "/api/lanes/"+itoa(laneID)+"/jobs", `{"task":"`+task+`","done_definition":"works"}`)
		var made map[string]any
		json.Unmarshal(w.Body.Bytes(), &made)
		ids[i] = int64(made["id"].(float64))
	}
	a.DB.Exec("UPDATE jobs SET state='done',finished_at=CURRENT_TIMESTAMP WHERE id=?", ids[0])
	a.DB.Exec("INSERT INTO job_runs(job_id,attempt,tmux_session,status,ended_at) VALUES(?,1,'completed-run','done',CURRENT_TIMESTAMP)", ids[0])

	w, _ = req(t, h, c, "POST", "/api/jobs/"+itoa(ids[0])+"/comment", `{"comment":"follow up"}`)
	if w.Code != 200 {
		t.Fatalf("done comment: %d %s", w.Code, w.Body.String())
	}
	var state string
	var position int
	if e = a.DB.QueryRow("SELECT state,position FROM jobs WHERE id=?", ids[0]).Scan(&state, &position); e != nil || state != "todo" || position != 3 {
		t.Fatalf("requeued job: state=%q position=%d err=%v", state, position, e)
	}

	w, _ = req(t, h, c, "GET", "/api/lanes", "")
	json.Unmarshal(w.Body.Bytes(), &lanes)
	var todoIDs []int64
	for _, job := range lanes[0].Jobs {
		if job.State == "todo" {
			todoIDs = append(todoIDs, job.ID)
		}
	}
	if len(todoIDs) != 3 || todoIDs[0] != ids[1] || todoIDs[1] != ids[2] || todoIDs[2] != ids[0] {
		t.Fatalf("todo display order=%v, want [%d %d %d]", todoIDs, ids[1], ids[2], ids[0])
	}
}

func TestReconcileBlocksRunningRunWithoutTmuxSession(t *testing.T) {
	a, e := Open(t.TempDir()+"/db", t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	_, c := req(t, a.Handler(), nil, "POST", "/api/auth/signup", `{"email":"reconcile@example.com","password":"password1"}`)
	w, _ := req(t, a.Handler(), c, "GET", "/api/lanes", "")
	var lanes []Lane
	json.Unmarshal(w.Body.Bytes(), &lanes)
	a.DB.Exec("UPDATE lanes SET paused=1 WHERE id=?", lanes[0].ID)
	w, _ = req(t, a.Handler(), c, "POST", "/api/lanes/"+itoa(lanes[0].ID)+"/jobs", `{"task":"hello"}`)
	var made map[string]any
	json.Unmarshal(w.Body.Bytes(), &made)
	id := int64(made["id"].(float64))
	a.DB.Exec("UPDATE jobs SET state='in_progress' WHERE id=?", id)
	a.DB.Exec("INSERT INTO job_runs(job_id,attempt,tmux_session,status) VALUES(?,1,?,'running')", id, "missing-session")

	a.reconcile()

	var runStatus, warning string
	a.DB.QueryRow("SELECT status FROM job_runs WHERE job_id=?", id).Scan(&runStatus)
	a.DB.QueryRow("SELECT warning FROM jobs WHERE id=?", id).Scan(&warning)
	if runStatus != "blocked" || warning != "Execution session missing after server restart" {
		t.Fatalf("status=%q warning=%q", runStatus, warning)
	}
	var statusEvents int
	a.DB.QueryRow(`SELECT count(*) FROM job_events e JOIN job_runs r ON r.id=e.job_run_id WHERE r.job_id=? AND e.kind='status' AND e.content LIKE '%in_progress%blocked%'`, id).Scan(&statusEvents)
	if statusEvents != 1 {
		t.Fatalf("blocked status timeline events=%d", statusEvents)
	}
}

func itoa(n int64) string {
	const d = "0123456789"
	if n == 0 {
		return "0"
	}
	b := make([]byte, 0, 20)
	for ; n > 0; n /= 10 {
		b = append([]byte{d[n%10]}, b...)
	}
	return string(b)
}
