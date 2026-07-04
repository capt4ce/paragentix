package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMarkdownSkills(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.md"), []byte("---\nname: go\n---\nUse Go."), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Use Go.") {
		t.Fatalf("skill text missing: %q", got)
	}
}
