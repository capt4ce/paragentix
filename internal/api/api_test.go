package api

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/capt4ce/custom-agent/internal/agent"
	"github.com/capt4ce/custom-agent/internal/config"
	"github.com/capt4ce/custom-agent/internal/storage"
)

func TestServeStartsResponseEndpoint(t *testing.T) {
	db, err := storage.Open(context.Background(), t.TempDir()+"/agent.db")
	if err != nil {
		t.Fatal(err)
	}
	a := agent.New(config.Config{Model: config.ModelConfig{APIKeyEnv: "MISSING"}, Profiles: map[string]config.Profile{"default": {Name: "default", FileAccess: "disabled"}}}, db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = Serve(ctx, "127.0.0.1:18080", a) }()
	time.Sleep(100 * time.Millisecond)
	res, err := http.Post("http://127.0.0.1:18080/v1/responses", "application/json", bytes.NewBufferString(`{"profile":"default","input":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status = %d", res.StatusCode)
	}
}
