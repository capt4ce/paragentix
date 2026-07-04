package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/capt4ce/custom-agent/internal/config"
)

func TestServerCallHTTPPostsJSON(t *testing.T) {
	var got map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	out, err := Server{Config: config.MCPServerConfig{Name: "http", Transport: "http", URL: ts.URL}}.Call(context.Background(), []byte(`{"ping":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"ok":true}` || got["ping"].(float64) != 1 {
		t.Fatalf("out=%s got=%v", out, got)
	}
}

func TestServerCallRejectsUnknownTransport(t *testing.T) {
	_, err := Server{Config: config.MCPServerConfig{Transport: "websocket"}}.Call(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
}
