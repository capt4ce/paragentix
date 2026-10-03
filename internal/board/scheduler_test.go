package board

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestScheduleFutureHeadBlocksLaneWhileAnotherLaneProceeds(t *testing.T) {
	releaseHermes := make(chan struct{})
	hermes := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-releaseHermes
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"review ready"}}]}`))
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

	req(t, a.Handler(), nil, http.MethodPost, "/api/auth/signup", `{"email":"scheduled-queue@example.com","password":"password1"}`)
	var user, board, project, firstLane int64
	if err = a.DB.QueryRow(`SELECT u.id,b.id,p.id,l.id FROM users u JOIN boards b ON b.user_id=u.id JOIN projects p ON p.workspace_id=b.workspace_id JOIN lanes l ON l.user_id=u.id WHERE u.email='scheduled-queue@example.com'`).Scan(&user, &board, &project, &firstLane); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec(`INSERT INTO columns(user_id,board_id,lane_id,project_id,name,position) VALUES(?,?,?,?,'Scheduled',0)`, user, board, firstLane, project); err != nil {
		t.Fatal(err)
	}
	a.DB.Exec(`UPDATE workspaces SET hermes_url=?,hermes_api_key='secret' WHERE user_id=?`, hermes.URL, user)
	res, err := a.DB.Exec(`INSERT INTO lanes(user_id,name,position) VALUES(?,'other lane',1)`, user)
	if err != nil {
		t.Fatal(err)
	}
	otherLane, _ := res.LastInsertId()
	if _, err = a.DB.Exec(`INSERT INTO columns(user_id,board_id,lane_id,project_id,name,position) VALUES(?,?,?,?,'Other',1)`, user, board, otherLane, project); err != nil {
		t.Fatal(err)
	}
	future := time.Now().UTC().Add(time.Hour).Format("2006-01-02 15:04:05")
	res, _ = a.DB.Exec(`INSERT INTO jobs(user_id,lane_id,task,position,scheduled_at) VALUES(?,?,'future head',0,?)`, user, firstLane, future)
	futureHead, _ := res.LastInsertId()
	res, _ = a.DB.Exec(`INSERT INTO jobs(user_id,lane_id,task,position) VALUES(?,?,'blocked follower',1)`, user, firstLane)
	follower, _ := res.LastInsertId()
	res, _ = a.DB.Exec(`INSERT INTO jobs(user_id,lane_id,task,position) VALUES(?,?,'other lane due',0)`, user, otherLane)
	otherJob, _ := res.LastInsertId()

	a.schedule()

	var headState, followerState, otherState string
	a.DB.QueryRow(`SELECT state FROM jobs WHERE id=?`, futureHead).Scan(&headState)
	a.DB.QueryRow(`SELECT state FROM jobs WHERE id=?`, follower).Scan(&followerState)
	a.DB.QueryRow(`SELECT state FROM jobs WHERE id=?`, otherJob).Scan(&otherState)
	if headState != "todo" || followerState != "todo" || otherState != "in_progress" {
		t.Fatalf("states head=%q follower=%q other=%q", headState, followerState, otherState)
	}

	if _, err = a.DB.Exec(`UPDATE jobs SET scheduled_at='2000-01-01 00:00:00' WHERE id=?`, futureHead); err != nil {
		t.Fatal(err)
	}
	a.schedule()
	var attempts, running int
	a.DB.QueryRow(`SELECT state,attempt_count FROM jobs WHERE id=?`, futureHead).Scan(&headState, &attempts)
	a.DB.QueryRow(`SELECT count(*) FROM job_runs WHERE job_id=? AND status='running'`, futureHead).Scan(&running)
	if headState != "in_progress" || attempts != 1 || running != 1 {
		t.Fatalf("due head state=%q attempts=%d running=%d", headState, attempts, running)
	}
}

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
