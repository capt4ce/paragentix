package board

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"root":"`+root+`"`) {
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
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"columns"`) {
		t.Fatalf("board alias: %d %s", w.Code, w.Body.String())
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
	a.DB.Exec("UPDATE jobs SET state='done' WHERE id=?", id)
	w, _ = req(t, h, c1, "PATCH", "/api/jobs/"+itoa(id), `{"done_definition":"changed"}`)
	if w.Code != 409 {
		t.Fatalf("done edit=%d", w.Code)
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
