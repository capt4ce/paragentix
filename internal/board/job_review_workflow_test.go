package board

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCopiedLegacyDatabaseMigratesWorkflowConstraintAndPreservesReferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	a, err := Open(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	req(t, a.Handler(), nil, "POST", "/api/auth/signup", `{"email":"legacy-workflow@example.com","password":"password1"}`)
	var user, lane int64
	a.DB.QueryRow(`SELECT id FROM users WHERE email='legacy-workflow@example.com'`).Scan(&user)
	a.DB.QueryRow(`SELECT id FROM lanes WHERE user_id=?`, user).Scan(&lane)
	a.DB.Exec(`UPDATE lanes SET paused=1 WHERE id=?`, lane)
	res, err := a.DB.Exec(`INSERT INTO jobs(user_id,lane_id,title,task,state,phase,position) VALUES(?,?,'Current title','legacy task','todo','review',0)`, user, lane)
	if err != nil {
		t.Fatal(err)
	}
	job, _ := res.LastInsertId()
	a.DB.Exec(`INSERT INTO job_conversations(job_id,title,status) VALUES(?,'Main','active')`, job)
	a.Close()

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`PRAGMA foreign_keys=OFF;
CREATE TABLE jobs_legacy(id INTEGER PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users ON DELETE CASCADE,lane_id INTEGER NOT NULL REFERENCES lanes ON DELETE CASCADE,task TEXT NOT NULL,done_definition TEXT NOT NULL DEFAULT '',warning TEXT NOT NULL DEFAULT '',state TEXT NOT NULL DEFAULT 'todo' CHECK(state IN('todo','in_progress','blocked','done')),position INTEGER NOT NULL,attempt_count INTEGER NOT NULL DEFAULT 0,created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,started_at TEXT,finished_at TEXT,pending_comment TEXT NOT NULL DEFAULT '',archived INTEGER NOT NULL DEFAULT 0,UNIQUE(lane_id,position));
INSERT INTO jobs_legacy(id,user_id,lane_id,task,done_definition,warning,state,position,attempt_count,created_at,updated_at,started_at,finished_at,pending_comment,archived)
SELECT id,user_id,lane_id,task,done_definition,warning,state,position,attempt_count,created_at,updated_at,started_at,finished_at,pending_comment,archived FROM jobs;
DROP TABLE jobs;
ALTER TABLE jobs_legacy RENAME TO jobs;`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}

	a, err = Open(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	var title string
	if err = a.DB.QueryRow(`SELECT title FROM jobs WHERE id=?`, job).Scan(&title); err != nil || title != "legacy task" {
		t.Fatalf("migrated title=%q err=%v", title, err)
	}
	if _, err = a.DB.Exec(`UPDATE jobs SET state='in_review' WHERE id=?`, job); err != nil {
		t.Fatalf("in_review constraint was not migrated: %v", err)
	}
	var conversationJob int64
	if err = a.DB.QueryRow(`SELECT job_id FROM job_conversations WHERE job_id=?`, job).Scan(&conversationJob); err != nil || conversationJob != job {
		t.Fatalf("conversation reference=%d err=%v", conversationJob, err)
	}
	var broken int
	a.DB.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&broken)
	if broken != 0 {
		t.Fatalf("foreign key violations=%d", broken)
	}
}

func TestJobWorkflowMigrationAddsTitlePhaseReviewAndTelegramWithoutBreakingForeignKeys(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	for table, column := range map[string]string{
		"jobs":          "title",
		"workspaces":    "telegram_chat_id",
		"notifications": "job_title",
	} {
		var count int
		if err = a.DB.QueryRow(`SELECT count(*) FROM pragma_table_info(?) WHERE name=?`, table, column).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s.%s count=%d err=%v", table, column, count, err)
		}
	}
	var broken int
	if err = a.DB.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&broken); err != nil || broken != 0 {
		t.Fatalf("foreign key check=%d err=%v", broken, err)
	}
}

func TestFallbackJobTitleIsTrimmedAndBounded(t *testing.T) {
	if got := fallbackJobTitle("  Fix the scheduler\nwithout exposing secrets  "); got != "Fix the scheduler without exposing secrets" {
		t.Fatalf("fallback=%q", got)
	}
	if got := fallbackJobTitle(strings.Repeat("word ", 40)); utf8.RuneCountInString(got) > 80 || !strings.HasSuffix(got, "…") {
		t.Fatalf("bounded fallback=%q length=%d", got, utf8.RuneCountInString(got))
	}
}

func TestHermesTitleUsesSupportedSessionMetadataAndKeepsFallbackWhenAbsent(t *testing.T) {
	title := "Hermes-authored review title"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sessions/titled":
			json.NewEncoder(w).Encode(map[string]any{"session": map[string]any{"title": title}})
		case "/api/sessions/untitled":
			json.NewEncoder(w).Encode(map[string]any{"session": map[string]any{"title": nil}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	_, cookie := req(t, a.Handler(), nil, "POST", "/api/auth/signup", `{"email":"title@example.com","password":"password1"}`)
	var user, lane int64
	a.DB.QueryRow(`SELECT id FROM users WHERE email='title@example.com'`).Scan(&user)
	a.DB.QueryRow(`SELECT id FROM lanes WHERE user_id=?`, user).Scan(&lane)
	a.DB.Exec(`UPDATE lanes SET paused=1 WHERE id=?`, lane)
	a.DB.Exec(`INSERT INTO columns(user_id,board_id,lane_id,project_id,name,position)
		SELECT ?,b.id,?,p.id,'Title lane',0 FROM boards b JOIN projects p ON p.workspace_id=b.workspace_id WHERE b.user_id=? LIMIT 1`, user, lane, user)
	a.DB.Exec(`UPDATE workspaces SET hermes_url=?,hermes_api_key='server-secret' WHERE user_id=?`, server.URL, user)
	w, _ := req(t, a.Handler(), cookie, "POST", "/api/lanes/"+itoa(lane)+"/jobs", `{"task":"A deliberately much longer task prompt that should not become a notification body","doneDefinition":"review"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create=%d %s", w.Code, w.Body.String())
	}
	var created map[string]any
	json.Unmarshal(w.Body.Bytes(), &created)
	job := int64(created["id"].(float64))

	if err = a.syncHermesTitle(job, "titled"); err != nil {
		t.Fatal(err)
	}
	var got string
	a.DB.QueryRow(`SELECT title FROM jobs WHERE id=?`, job).Scan(&got)
	if got != title {
		t.Fatalf("title=%q", got)
	}
	if err = a.syncHermesTitle(job, "untitled"); err != nil {
		t.Fatal(err)
	}
	a.DB.QueryRow(`SELECT title FROM jobs WHERE id=?`, job).Scan(&got)
	if got != title {
		t.Fatalf("null metadata replaced title with %q", got)
	}
}

func TestReviewApprovalAndFeedbackTransitionsAreAtomicAndReuseSession(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	_, cookie := req(t, a.Handler(), nil, "POST", "/api/auth/signup", `{"email":"review@example.com","password":"password1"}`)
	var user, lane int64
	a.DB.QueryRow(`SELECT id FROM users WHERE email='review@example.com'`).Scan(&user)
	a.DB.QueryRow(`SELECT id FROM lanes WHERE user_id=?`, user).Scan(&lane)
	a.DB.Exec(`UPDATE lanes SET paused=1 WHERE id=?`, lane)
	res, err := a.DB.Exec(`INSERT INTO jobs(user_id,lane_id,title,task,state,phase,position,attempt_count) VALUES(?,?,'Review title','private prompt','in_review','review',0,1)`, user, lane)
	if err != nil {
		t.Fatal(err)
	}
	job, _ := res.LastInsertId()
	res, _ = a.DB.Exec(`INSERT INTO job_runs(job_id,attempt,tmux_session,status,ended_at) VALUES(?,1,'hermes-api:same-session','done',CURRENT_TIMESTAMP)`, job)
	run, _ := res.LastInsertId()
	a.DB.Exec(`INSERT INTO job_conversations(job_id,title,status,hermes_session_id) VALUES(?,'Main','active','same-session')`, job)

	w, _ := req(t, a.Handler(), cookie, "POST", "/api/jobs/"+itoa(job)+"/comment", `{"comment":"Please revise the plan"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("feedback=%d %s", w.Code, w.Body.String())
	}
	var state, session, pending string
	var position int
	a.DB.QueryRow(`SELECT state,pending_comment,position FROM jobs WHERE id=?`, job).Scan(&state, &pending, &position)
	a.DB.QueryRow(`SELECT tmux_session FROM job_runs WHERE id=?`, run).Scan(&session)
	if state != "todo" || pending != "Please revise the plan" || session != "hermes-api:same-session" {
		t.Fatalf("feedback state=%q pending=%q session=%q position=%d", state, pending, session, position)
	}

	a.DB.Exec(`UPDATE jobs SET state='in_review',pending_comment='' WHERE id=?`, job)
	w, _ = req(t, a.Handler(), cookie, "POST", "/api/jobs/"+itoa(job)+"/approve", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("approve=%d %s", w.Code, w.Body.String())
	}
	a.DB.QueryRow(`SELECT state,phase FROM jobs WHERE id=?`, job).Scan(&state, &pending)
	a.DB.QueryRow(`SELECT tmux_session FROM job_runs WHERE id=?`, run).Scan(&session)
	if state != "todo" || pending != "implementation" || session != "hermes-api:same-session" {
		t.Fatalf("approval state=%q phase=%q session=%q", state, pending, session)
	}
	w, _ = req(t, a.Handler(), cookie, "POST", "/api/jobs/"+itoa(job)+"/approve", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("idempotent approve=%d %s", w.Code, w.Body.String())
	}
}

func TestCompletedJobFeedbackStartsANewReviewPhase(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	_, cookie := req(t, a.Handler(), nil, "POST", "/api/auth/signup", `{"email":"completed-feedback@example.com","password":"password1"}`)
	var user, lane int64
	a.DB.QueryRow(`SELECT id FROM users WHERE email='completed-feedback@example.com'`).Scan(&user)
	a.DB.QueryRow(`SELECT id FROM lanes WHERE user_id=?`, user).Scan(&lane)
	a.DB.Exec(`UPDATE lanes SET paused=1 WHERE id=?`, lane)
	res, err := a.DB.Exec(`INSERT INTO jobs(user_id,lane_id,title,task,state,phase,position,attempt_count) VALUES(?,?,'Review again','private prompt','done','implementation',0,1)`, user, lane)
	if err != nil {
		t.Fatal(err)
	}
	job, _ := res.LastInsertId()
	res, _ = a.DB.Exec(`INSERT INTO job_runs(job_id,attempt,tmux_session,status,ended_at) VALUES(?,1,'hermes-api:same-session','done',CURRENT_TIMESTAMP)`, job)
	run, _ := res.LastInsertId()

	w, _ := req(t, a.Handler(), cookie, "POST", "/api/jobs/"+itoa(job)+"/comment", `{"comment":"The last result is wrong; propose a correction"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("feedback=%d %s", w.Code, w.Body.String())
	}
	var state, phase string
	a.DB.QueryRow(`SELECT state,phase FROM jobs WHERE id=?`, job).Scan(&state, &phase)
	if state != "todo" || phase != "review" {
		t.Fatalf("requeued state=%q phase=%q, want todo/review", state, phase)
	}

	a.DB.Exec(`UPDATE jobs SET state='in_progress' WHERE id=?`, job)
	a.DB.Exec(`UPDATE job_runs SET status='running',ended_at=NULL WHERE id=?`, run)
	if err = a.finishHermesRun(job, run, "Revised proposal only; no implementation was performed."); err != nil {
		t.Fatal(err)
	}
	a.DB.QueryRow(`SELECT state,phase FROM jobs WHERE id=?`, job).Scan(&state, &phase)
	if state != "in_review" || phase != "review" {
		t.Fatalf("proposal state=%q phase=%q, want in_review/review", state, phase)
	}
}

func TestApprovalReplyUsesApprovalTransition(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	_, cookie := req(t, a.Handler(), nil, "POST", "/api/auth/signup", `{"email":"reply-approval@example.com","password":"password1"}`)
	var user, lane int64
	a.DB.QueryRow(`SELECT id FROM users WHERE email='reply-approval@example.com'`).Scan(&user)
	a.DB.QueryRow(`SELECT id FROM lanes WHERE user_id=?`, user).Scan(&lane)
	a.DB.Exec(`UPDATE lanes SET paused=1 WHERE id=?`, lane)
	res, err := a.DB.Exec(`INSERT INTO jobs(user_id,lane_id,title,task,state,phase,position,attempt_count) VALUES(?,?,'Approve by reply','private prompt','in_review','review',0,1)`, user, lane)
	if err != nil {
		t.Fatal(err)
	}
	job, _ := res.LastInsertId()
	res, _ = a.DB.Exec(`INSERT INTO job_runs(job_id,attempt,tmux_session,status,ended_at) VALUES(?,1,'hermes-api:same-session','done',CURRENT_TIMESTAMP)`, job)
	run, _ := res.LastInsertId()

	w, _ := req(t, a.Handler(), cookie, "POST", "/api/jobs/"+itoa(job)+"/comment", `{"comment":"Go ahead with the plan"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("approval reply=%d %s", w.Code, w.Body.String())
	}
	var state, phase, status string
	a.DB.QueryRow(`SELECT state,phase FROM jobs WHERE id=?`, job).Scan(&state, &phase)
	a.DB.QueryRow(`SELECT status FROM job_runs WHERE id=?`, run).Scan(&status)
	if state != "todo" || phase != "implementation" || status != "done" {
		t.Fatalf("approval state=%q phase=%q run=%q", state, phase, status)
	}
	var comments, approvals int
	a.DB.QueryRow(`SELECT count(*) FROM job_events WHERE job_run_id=? AND kind='comment' AND content='Go ahead with the plan'`, run).Scan(&comments)
	a.DB.QueryRow(`SELECT count(*) FROM job_events WHERE job_run_id=? AND kind='approval'`, run).Scan(&approvals)
	if comments != 1 || approvals != 1 {
		t.Fatalf("approval reply events: comments=%d approvals=%d", comments, approvals)
	}
}

func TestNotificationsExposeActionAndTitleButNeverPrompt(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	_, cookie := req(t, a.Handler(), nil, "POST", "/api/auth/signup", `{"email":"notify-title@example.com","password":"password1"}`)
	var user, lane int64
	a.DB.QueryRow(`SELECT id FROM users WHERE email='notify-title@example.com'`).Scan(&user)
	a.DB.QueryRow(`SELECT id FROM lanes WHERE user_id=?`, user).Scan(&lane)
	res, _ := a.DB.Exec(`INSERT INTO jobs(user_id,lane_id,title,task,state,position) VALUES(?,?,'Safe title','secret prompt','in_review',0)`, user, lane)
	job, _ := res.LastInsertId()
	res, _ = a.DB.Exec(`INSERT INTO job_runs(job_id,attempt,tmux_session,status) VALUES(?,1,'hermes-api:s','done')`, job)
	run, _ := res.LastInsertId()
	a.notify(job, run, "review")
	a.notify(job, run, "review")

	w, _ := req(t, a.Handler(), cookie, "GET", "/api/notifications", "")
	body := w.Body.String()
	if !strings.Contains(body, `"action":"Ready for review"`) || !strings.Contains(body, `"job_title":"Safe title"`) || strings.Contains(body, "secret prompt") {
		t.Fatalf("notification=%s", body)
	}
	var reviews int
	a.DB.QueryRow(`SELECT count(*) FROM notifications WHERE job_id=? AND kind='review'`, job).Scan(&reviews)
	if reviews != 2 {
		t.Fatalf("review lifecycle notifications=%d, want 2", reviews)
	}
}

func TestTelegramSettingsNeverExposeTokenAndTestDeliveryUsesServerToken(t *testing.T) {
	var gotPath string
	var got map[string]any
	reject := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&got)
		if reject {
			http.Error(w, "server-token", http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer server.Close()
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.TelegramBotToken = "server-token"
	a.TelegramAPIBase = server.URL
	_, cookie := req(t, a.Handler(), nil, "POST", "/api/auth/signup", `{"email":"telegram@example.com","password":"password1"}`)
	var workspace int64
	a.DB.QueryRow(`SELECT id FROM workspaces WHERE user_id=(SELECT id FROM users WHERE email='telegram@example.com')`).Scan(&workspace)
	w, _ := req(t, a.Handler(), cookie, "PATCH", "/api/workspaces/"+itoa(workspace)+"/settings", `{"hermes_url":"http://hermes.test","hermes_api_key":"secret","hermes_model":"hermes-agent","telegram_enabled":true,"telegram_chat_id":"12345","telegram_bot_token":"browser-token"}`)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "server-token") || strings.Contains(w.Body.String(), "browser-token") {
		t.Fatalf("settings=%d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, a.Handler(), cookie, "POST", "/api/workspaces/"+itoa(workspace)+"/settings/telegram-test", `{}`)
	if w.Code != http.StatusOK || gotPath != "/botserver-token/sendMessage" || got["chat_id"] != "12345" {
		t.Fatalf("test=%d %s path=%q payload=%v", w.Code, w.Body.String(), gotPath, got)
	}
	reject = true
	w, _ = req(t, a.Handler(), cookie, "POST", "/api/workspaces/"+itoa(workspace)+"/settings/telegram-test", `{}`)
	if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "server-token") {
		t.Fatalf("rejected test exposed credentials: %d %s", w.Code, w.Body.String())
	}
}
