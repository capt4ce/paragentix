package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/capt4ce/paragentix/internal/config"
	"github.com/capt4ce/paragentix/internal/llm"
	"github.com/capt4ce/paragentix/internal/storage"
)

type fakeProvider struct{ seen []llm.Message }

func (f *fakeProvider) Chat(_ context.Context, messages []llm.Message, _ []llm.ToolSchema) (llm.ChatResponse, error) {
	f.seen = messages
	return llm.ChatResponse{Content: "done"}, nil
}

func TestRunPersistsSessionMessagesAndInjectsMemory(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, t.TempDir()+"/agent.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.UpsertMemory(ctx, storage.Memory{ID: "m1", ProfileID: "default", Scope: "user", Content: "User likes terse answers"}); err != nil {
		t.Fatal(err)
	}
	fp := &fakeProvider{}
	a := NewWithProvider(config.Config{Profiles: map[string]config.Profile{"default": {Name: "default", FileAccess: "disabled"}}}, db, fp)
	res, err := a.Run(ctx, Request{Profile: "default", Input: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionID == "" || res.Output != "done" {
		t.Fatalf("bad response: %+v", res)
	}
	msgs, err := db.ListMessages(ctx, res.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[1].Role != "assistant" {
		t.Fatalf("messages not persisted: %+v", msgs)
	}
	if !strings.Contains(fp.seen[0].Content, "User likes terse answers") {
		t.Fatalf("memory missing from prompt: %q", fp.seen[0].Content)
	}
}
