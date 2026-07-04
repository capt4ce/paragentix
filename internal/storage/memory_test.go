package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMemoryUpsertAndList(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	m := Memory{ID: "m1", ProfileID: "default", Scope: "user", Content: "likes concise answers", Tags: `["pref"]`}
	if err := db.UpsertMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	m.Content = "likes direct answers"
	if err := db.UpsertMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	got, err := db.ListMemories(ctx, "default", "user")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Content != "likes direct answers" || got[0].CreatedAt == "" || got[0].UpdatedAt == "" {
		t.Fatalf("unexpected memories: %+v", got)
	}
}

func TestSessionMessagesRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := db.CreateSession(ctx, Session{ID: "s1", ProfileID: "default", Source: "cli", Title: "hello"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AppendMessage(ctx, Message{SessionID: "s1", Role: "user", Content: []byte(`{"content":"hi"}`)}); err != nil {
		t.Fatal(err)
	}
	msgs, err := db.ListMessages(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Role != "user" || string(msgs[0].Content) != `{"content":"hi"}` {
		t.Fatalf("unexpected messages: %+v", msgs)
	}
}
