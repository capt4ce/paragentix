package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/capt4ce/custom-agent/internal/config"
)

func TestRegistryRunsConfiguredMCPServer(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":"pong"}`))
	}))
	defer ts.Close()

	r := NewRegistry(config.Config{MCPServers: []config.MCPServerConfig{{Name: "ping", Transport: "http", URL: ts.URL}}})
	args, _ := json.Marshal(map[string]any{"server": "ping", "payload": map[string]any{"method": "tools/list"}})
	res := r.Run(context.Background(), config.Profile{Name: "default"}, "mcp_call", args)
	if !res.OK || res.Output != `{"result":"pong"}` {
		t.Fatalf("res = %+v", res)
	}
}

func TestRegistryRejectsUnknownMCPServer(t *testing.T) {
	r := NewRegistry(config.Config{})
	res := r.Run(context.Background(), config.Profile{Name: "default"}, "mcp_call", json.RawMessage(`{"server":"missing"}`))
	if res.OK || res.Error == "" {
		t.Fatalf("res = %+v", res)
	}
}
