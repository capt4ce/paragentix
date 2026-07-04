package skills

import (
	"os"
	"path/filepath"
	"strings"
)

func Load(dir string) (string, error) {
	if dir == "" {
		return "", nil
	}
	var parts []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		parts = append(parts, string(b))
		return nil
	})
	return strings.Join(parts, "\n\n---\n\n"), err
}
