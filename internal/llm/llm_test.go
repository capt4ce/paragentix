package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/capt4ce/paragentix/internal/config"
)

func TestOpenAICompatibleSendsToolsAndParsesToolCalls(t *testing.T) {
	t.Setenv("TEST_KEY", "secret")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("bad request: %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"","tool_calls":[{"id":"c1","function":{"name":"file_read","arguments":"{\"path\":\"x\"}"}}]}}]}`))
	}))
	defer ts.Close()
	res, err := OpenAICompatible{Config: config.ModelConfig{Model: "m", BaseURL: ts.URL, APIKeyEnv: "TEST_KEY"}, Client: ts.Client()}.Chat(context.Background(), nil, []ToolSchema{{Name: "file_read"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "file_read" {
		t.Fatalf("bad response: %+v", res)
	}
}

func TestNewProviderNormalizesDeepSeekAndOpenAICompatible(t *testing.T) {
	if _, ok := NewProvider(config.ModelConfig{Provider: "deepseek"}).(OpenAICompatible); !ok {
		t.Fatal("deepseek should use OpenAI-compatible adapter")
	}
	if _, ok := NewProvider(config.ModelConfig{Provider: "openai-compatible"}).(OpenAICompatible); !ok {
		t.Fatal("openai-compatible should use OpenAI-compatible adapter")
	}
}

func TestCodexProviderUsesConfiguredCommand(t *testing.T) {
	dir := t.TempDir()
	cmd := dir + "/codex-fake"
	if err := os.WriteFile(cmd, []byte("#!/bin/sh\nprintf '{\"content\":\"ok\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := CodexProvider{Config: config.ModelConfig{Command: cmd}}.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil || strings.TrimSpace(res.Content) != "ok" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}
