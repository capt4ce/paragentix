package board

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestScheduleStartsNextTodoWhenPredecessorIsInReview(t *testing.T) {
	releaseHermes := make(chan struct{})
	hermes := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions" {
			<-releaseHermes
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"choices":[{"message":{"content":"review ready"}}]}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[]}`))
	}))

	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		hermes.Close()
		t.Fatal(err)
	}
	defer func() {
		close(releaseHermes)
		a.Close()
		hermes.Close()
	}()

	req(t, a.Handler(), nil, "POST", "/api/auth/signup", `{"email":"schedule-review@example.com","password":"password1"}`)
	var user, lane int64
	if err = a.DB.QueryRow(`SELECT u.id,l.id FROM users u JOIN lanes l ON l.user_id=u.id WHERE u.email='schedule-review@example.com'`).Scan(&user, &lane); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec(`UPDATE lanes SET paused=1 WHERE id=?`, lane); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec(`INSERT INTO columns(user_id,board_id,lane_id,project_id,name,position)
		SELECT ?,b.id,?,p.id,'Schedule lane',0 FROM boards b JOIN projects p ON p.workspace_id=b.workspace_id WHERE b.user_id=? LIMIT 1`, user, lane, user); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec(`UPDATE workspaces SET hermes_url=?,hermes_api_key='secret' WHERE user_id=?`, hermes.URL, user); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec(`INSERT INTO jobs(user_id,lane_id,task,state,position,attempt_count) VALUES(?,?,'awaiting review','in_review',0,1)`, user, lane); err != nil {
		t.Fatal(err)
	}
	res, err := a.DB.Exec(`INSERT INTO jobs(user_id,lane_id,task,state,position) VALUES(?,?,'next queued job','todo',1)`, user, lane)
	if err != nil {
		t.Fatal(err)
	}
	nextJob, _ := res.LastInsertId()
	if _, err = a.DB.Exec(`UPDATE lanes SET paused=0 WHERE id=?`, lane); err != nil {
		t.Fatal(err)
	}

	a.schedule()

	var state string
	var attempts, runningRuns int
	if err = a.DB.QueryRow(`SELECT state,attempt_count FROM jobs WHERE id=?`, nextJob).Scan(&state, &attempts); err != nil {
		t.Fatal(err)
	}
	if err = a.DB.QueryRow(`SELECT count(*) FROM job_runs WHERE job_id=? AND status='running'`, nextJob).Scan(&runningRuns); err != nil {
		t.Fatal(err)
	}
	if state != "in_progress" || attempts != 1 || runningRuns != 1 {
		t.Fatalf("next job state=%q attempts=%d running runs=%d; want in_progress, 1, 1", state, attempts, runningRuns)
	}
}
