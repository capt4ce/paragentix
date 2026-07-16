package board

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
