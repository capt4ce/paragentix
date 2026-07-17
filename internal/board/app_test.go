package board

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"projectDirectory":"`+root+`"`) {
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
	if w.Code != 200 || w.Body.String() != "[]\n" {
		t.Fatalf("board alias: %d %s", w.Code, w.Body.String())
	}
}

func TestCodexJobPromptIsPassedAsExecArgument(t *testing.T) {
	prompt := "Implement responsive design\n\nDone definition:\nWorks on mobile"
	got, sendKeys := jobCommand([]string{"codex", "-m", "gpt-5.6", "--yolo"}, "codex", prompt)
	want := []string{"codex", "-m", "gpt-5.6", "--yolo", "exec", prompt}
	if strings.Join(got, "|") != strings.Join(want, "|") || sendKeys {
		t.Fatalf("command=%q sendKeys=%v", got, sendKeys)
	}
}

func TestV2CommandLexerAndAdditiveMigration(t *testing.T) {
	got, err := parseCommand(`codex -m "gpt 5" --flag='literal;$(x)' escaped\ value`)
	want := []string{"codex", "-m", "gpt 5", "--flag=literal;$(x)", "escaped value"}
	if err != nil || strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("parse=%q err=%v", got, err)
	}
	for _, bad := range []string{"", `codex "unfinished`, "codex \\", "codex\x00bad"} {
		if _, err := parseCommand(bad); err == nil {
			t.Fatalf("accepted malformed %q", bad)
		}
	}
	db := filepath.Join(t.TempDir(), "existing.db")
	a, err := Open(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec(`INSERT INTO users(email,password_hash) VALUES('legacy@example.com',x'00')`); err != nil {
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
	w, _ = req(t, h, cookie, "POST", "/api/boards/"+itoa(boardID)+"/columns", `{"worktreeEnabled":false}`)
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
	if json.Unmarshal(w.Body.Bytes(), &columns) != nil || len(columns) != 1 || len(columns[0].Jobs) != 1 || columns[0].Jobs[0].Task != "mapped" {
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
	if w.Code != 200 {
		t.Fatalf("retry done=%d %s", w.Code, w.Body.String())
	}
	var state string
	var finished any
	if e := a.DB.QueryRow("SELECT state,finished_at FROM jobs WHERE id=?", id).Scan(&state, &finished); e != nil || state != "todo" || finished != nil {
		t.Fatalf("retried job state=%q finished=%v err=%v", state, finished, e)
	}
	w, _ = req(t, h, c1, "POST", "/api/jobs/"+itoa(id)+"/retry", `{}`)
	if w.Code != 409 {
		t.Fatalf("retry todo=%d", w.Code)
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
	a.DB.Exec("UPDATE jobs SET state='done' WHERE id=?", id)
	w, _ = req(t, h, c, "POST", "/api/jobs/"+itoa(id)+"/comment", `{"comment":"late"}`)
	if w.Code != 409 {
		t.Fatalf("done comment=%d", w.Code)
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
	a.DB.Exec("UPDATE jobs SET state='blocked' WHERE id=?", id)
	a.DB.Exec("INSERT INTO job_runs(job_id,attempt,tmux_session,status) VALUES(?,1,?,'running')", id, "missing-session")

	a.reconcile()

	var runStatus, warning string
	a.DB.QueryRow("SELECT status FROM job_runs WHERE job_id=?", id).Scan(&runStatus)
	a.DB.QueryRow("SELECT warning FROM jobs WHERE id=?", id).Scan(&warning)
	if runStatus != "blocked" || warning != "Execution session missing after server restart" {
		t.Fatalf("status=%q warning=%q", runStatus, warning)
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
