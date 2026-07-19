package board

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestEmbeddedFrontendIncludesWorkspaceUserStatuses(t *testing.T) {
	index, err := fs.ReadFile(web, "web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`assets/(index-[^"']+\.js)`).FindSubmatch(index)
	if len(match) != 2 {
		t.Fatalf("embedded frontend index has no JavaScript asset: %s", index)
	}
	asset, err := fs.ReadFile(web, "web/assets/"+string(match[1]))
	if err != nil {
		t.Fatalf("embedded frontend asset %q: %v", match[1], err)
	}
	for _, label := range []string{"Invited", "Member"} {
		if !strings.Contains(string(asset), label) {
			t.Fatalf("embedded frontend asset %q does not include workspace user status %q", match[1], label)
		}
	}
}

type testMailer struct{ body *string }

func (m testMailer) Send(_, _, body string) error {
	if m.body != nil {
		*m.body = body
	}
	return nil
}

func TestResendMailer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer test-key" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("request method=%q auth=%q content-type=%q", r.Method, r.Header.Get("Authorization"), r.Header.Get("Content-Type"))
		}
		var payload struct {
			From, Subject, Text string
			To                  []string
		}
		if e := json.NewDecoder(r.Body).Decode(&payload); e != nil {
			t.Fatal(e)
		}
		if payload.From != "Paragentix <invites@example.test>" || len(payload.To) != 1 || payload.To[0] != "user@example.test" || payload.Subject != "Workspace invitation" || payload.Text != "https://example.test/?invite=token" {
			t.Fatalf("payload=%+v", payload)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	m := ResendMailer{APIKey: "test-key", From: "Paragentix <invites@example.test>", URL: server.URL, Client: server.Client()}
	if e := m.Send("user@example.test", "Workspace invitation", "https://example.test/?invite=token"); e != nil {
		t.Fatal(e)
	}
}

func TestInvitationURLUsesRequestOriginByDefault(t *testing.T) {
	a, e := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	var body string
	a.Mailer = testMailer{&body}
	h := a.Handler()
	_, owner := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"owner@x.test","password":"password1"}`)
	w, _ := req(t, h, owner, "POST", "/api/workspaces", `{"name":"Team"}`)
	var workspace map[string]any
	json.Unmarshal(w.Body.Bytes(), &workspace)

	r := httptest.NewRequest("POST", "http://app.example.test/api/workspaces/"+itoa(int64(workspace["id"].(float64)))+"/invitations", strings.NewReader(`{"email":"new@x.test"}`))
	r.Header.Set("X-Forwarded-Proto", "https")
	if r.TLS != nil {
		t.Fatal("reverse-proxy request unexpectedly has TLS state")
	}
	r.AddCookie(owner)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated || !strings.HasPrefix(body, "https://app.example.test/?invite=") {
		t.Fatalf("invite=%d url=%q", w.Code, body)
	}
}

func TestInvitationURLAndReinviteAfterExpiry(t *testing.T) {
	a, e := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	var body string
	a.Mailer, a.BaseURL = testMailer{&body}, "https://example.test/paragentix/"
	h := a.Handler()
	_, owner := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"owner2@x.test","password":"password1"}`)
	w, _ := req(t, h, owner, "POST", "/api/workspaces", `{"name":"Team"}`)
	var workspace map[string]any
	json.Unmarshal(w.Body.Bytes(), &workspace)
	wid := int64(workspace["id"].(float64))
	path := "/api/workspaces/" + itoa(wid) + "/invitations"
	w, _ = req(t, h, owner, "POST", path, `{"email":"new@x.test"}`)
	if w.Code != 201 || !strings.HasPrefix(body, "https://example.test/paragentix/?invite=") {
		t.Fatalf("invite=%d url=%q", w.Code, body)
	}
	w, _ = req(t, h, owner, "POST", path, `{"email":"new@x.test"}`)
	if w.Code != 409 {
		t.Fatalf("active duplicate=%d", w.Code)
	}
	if _, e = a.DB.Exec(`UPDATE workspace_invitations SET expires_at=datetime('now','-1 second') WHERE workspace_id=?`, wid); e != nil {
		t.Fatal(e)
	}
	w, _ = req(t, h, owner, "POST", path, `{"email":"new@x.test"}`)
	if w.Code != 201 {
		t.Fatalf("reinvite=%d %s", w.Code, w.Body.String())
	}
	var n int
	if e = a.DB.QueryRow(`SELECT count(*) FROM workspace_invitations WHERE workspace_id=? AND email=? AND accepted_at IS NULL`, wid, "new@x.test").Scan(&n); e != nil || n != 1 {
		t.Fatalf("active rows=%d err=%v", n, e)
	}
}

func TestWorkspaceProjectsMembershipInvitesAndColumnProject(t *testing.T) {
	root := t.TempDir()
	a, e := Open(filepath.Join(t.TempDir(), "db"), root)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	a.Mailer = testMailer{}
	h := a.Handler()
	_, owner := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"owner@x.test","password":"password1"}`)
	_, member := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"member@x.test","password":"password1"}`)
	w, _ := req(t, h, owner, "POST", "/api/workspaces", `{"name":"Team"}`)
	if w.Code != 201 {
		t.Fatalf("workspace %d %s", w.Code, w.Body.String())
	}
	var x map[string]any
	json.Unmarshal(w.Body.Bytes(), &x)
	wid := int64(x["id"].(float64))
	w, _ = req(t, h, owner, "POST", "/api/workspaces/"+itoa(wid)+"/invitations", `{"email":"member@x.test"}`)
	if w.Code != 201 {
		t.Fatalf("invite %d %s", w.Code, w.Body.String())
	}
	var invite map[string]any
	json.Unmarshal(w.Body.Bytes(), &invite)
	tok, _ := invite["token"].(string)
	if tok == "" {
		t.Fatal("test mail seam did not return token")
	}
	w, _ = req(t, h, owner, "GET", "/api/workspaces/"+itoa(wid)+"/members", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"email":"member@x.test"`) || !strings.Contains(w.Body.String(), `"status":"invited"`) {
		t.Fatalf("invited user list %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, member, "POST", "/api/invitations/"+tok, "{}")
	if w.Code != 200 {
		t.Fatalf("accept %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, owner, "GET", "/api/workspaces/"+itoa(wid)+"/members", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"email":"member@x.test"`) || !strings.Contains(w.Body.String(), `"status":"member"`) || strings.Contains(w.Body.String(), `"status":"invited"`) {
		t.Fatalf("accepted user list %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, member, "GET", "/api/workspaces", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Team") {
		t.Fatalf("member list %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, member, "PATCH", "/api/workspaces/"+itoa(wid), `{"name":"No"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("member managed workspace: %d", w.Code)
	}
	projectDir := filepath.Join(root, "app")
	if e = os.Mkdir(projectDir, 0755); e != nil {
		t.Fatal(e)
	}
	w, _ = req(t, h, owner, "POST", "/api/workspaces/"+itoa(wid)+"/projects", `{"name":"App","directory":"app"}`)
	if w.Code != 201 {
		t.Fatalf("relative project %d %s", w.Code, w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &x)
	pid := int64(x["id"].(float64))
	w, _ = req(t, h, owner, "POST", "/api/workspaces/"+itoa(wid)+"/projects", `{"name":"Duplicate","directory":"app"}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "directory is already used by App") {
		t.Fatalf("duplicate project directory %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, owner, "POST", "/api/boards", `{"name":"Board","workspaceId":`+itoa(wid)+`}`)
	json.Unmarshal(w.Body.Bytes(), &x)
	bid := int64(x["id"].(float64))
	w, _ = req(t, h, owner, "POST", "/api/boards/"+itoa(bid)+"/columns", `{"name":"Todo","projectId":`+itoa(pid)+`,"worktreeEnabled":false}`)
	if w.Code != 201 || !strings.Contains(w.Body.String(), "projectId") {
		t.Fatalf("column %d %s", w.Code, w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &x)
	cid := int64(x["id"].(float64))
	w, _ = req(t, h, owner, "POST", "/api/columns/"+itoa(cid)+"/jobs", `{"task":"Fix project search","doneDefinition":"Tests pass"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("project job %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, member, "GET", "/api/projects", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"name":"App"`) || !strings.Contains(w.Body.String(), `"jobCount":1`) {
		t.Fatalf("member project list %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, member, "GET", "/api/projects/"+itoa(pid), "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"task":"Fix project search"`) || !strings.Contains(w.Body.String(), `"boardName":"Board"`) || !strings.Contains(w.Body.String(), `"columnName":"Todo"`) {
		t.Fatalf("member project detail %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, member, "GET", "/api/boards/"+itoa(bid)+"/columns", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"projectName":"App"`) {
		t.Fatalf("member columns %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, member, "PATCH", "/api/columns/"+itoa(cid), `{"name":"Review","projectId":`+itoa(pid)+`}`)
	if w.Code != 200 {
		t.Fatalf("member column patch %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, member, "GET", "/api/boards/"+itoa(bid)+"/columns", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"Review"`) {
		t.Fatalf("column patch did not persist name: %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, owner, "PATCH", "/api/projects/"+itoa(pid), `{"name":"Renamed","directory":"`+root+`"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Renamed") {
		t.Fatalf("project patch %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, owner, "DELETE", "/api/workspaces/"+itoa(wid)+"/members/1", "")
	if w.Code != 409 {
		t.Fatalf("self/final owner removal %d", w.Code)
	}
}

func TestMigrationCreatesDefaultProjectIdempotently(t *testing.T) {
	db := filepath.Join(t.TempDir(), "db")
	root := t.TempDir()
	a, e := Open(db, root)
	if e != nil {
		t.Fatal(e)
	}
	_, c := req(t, a.Handler(), nil, "POST", "/api/auth/signup", `{"email":"m@x.test","password":"password1"}`)
	_ = c
	a.Close()
	a, e = Open(db, root)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	var n int
	if e = a.DB.QueryRow(`SELECT count(*) FROM projects WHERE name='Default Project'`).Scan(&n); e != nil || n != 1 {
		t.Fatalf("default projects=%d err=%v", n, e)
	}
}
