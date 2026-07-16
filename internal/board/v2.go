package board

import (
	"errors"
	"path/filepath"
	"strings"
)

// parseCommand applies POSIX-like quoting/escaping without expansion or a shell.
func parseCommand(s string) ([]string, error) {
	if strings.IndexByte(s, 0) >= 0 {
		return nil, errors.New("NUL is not allowed")
	}
	var out []string
	var b strings.Builder
	var quote rune
	escaped, started := false, false
	flush := func() {
		if started {
			out = append(out, b.String())
			b.Reset()
			started = false
		}
	}
	for _, r := range s {
		if escaped {
			b.WriteRune(r)
			escaped = false
			started = true
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			started = true
			continue
		}
		if r == '\'' || r == '"' {
			if quote == 0 {
				quote = r
				started = true
				continue
			}
			if quote == r {
				quote = 0
				continue
			}
		}
		if strings.ContainsRune(" \t\n", r) && quote == 0 {
			flush()
			continue
		}
		b.WriteRune(r)
		started = true
	}
	if escaped || quote != 0 {
		return nil, errors.New("unfinished quote or escape")
	}
	flush()
	if len(out) == 0 || out[0] == "" {
		return nil, errors.New("command is empty")
	}
	return out, nil
}

func contained(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
