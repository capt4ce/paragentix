package board

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	maxAttachments    = 20
	maxAttachmentSize = 20 << 20
	maxUploadSize     = maxAttachments*maxAttachmentSize + (1 << 20)
)

type jobAttachment struct {
	Name, Content string
}

func parseAttachments(w http.ResponseWriter, r *http.Request) ([]jobAttachment, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		return nil, fmt.Errorf("upload is too large or invalid")
	}
	defer r.MultipartForm.RemoveAll()
	files := r.MultipartForm.File["files"]
	if len(files) > maxAttachments {
		return nil, fmt.Errorf("at most %d files may be attached", maxAttachments)
	}
	attachments := make([]jobAttachment, 0, len(files))
	seen := map[string]bool{}
	for _, header := range files {
		name := header.Filename
		if name == "" || name != filepath.Base(name) || strings.ContainsAny(name, "\x00\r\n") || seen[name] {
			return nil, fmt.Errorf("attachment names must be unique, valid file names")
		}
		seen[name] = true
		file, err := header.Open()
		if err != nil {
			return nil, fmt.Errorf("could not read attachment")
		}
		content, readErr := io.ReadAll(io.LimitReader(file, maxAttachmentSize+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return nil, fmt.Errorf("could not read attachment")
		}
		if len(content) > maxAttachmentSize {
			return nil, fmt.Errorf("each attachment must be 20 MB or smaller")
		}
		stored := string(content)
		if !utf8.Valid(content) || strings.IndexByte(stored, 0) >= 0 {
			stored = "Base64-encoded content:\n" + base64.StdEncoding.EncodeToString(content)
		}
		attachments = append(attachments, jobAttachment{Name: name, Content: stored})
	}
	return attachments, nil
}

func appendAttachmentContext(message string, attachments []jobAttachment) string {
	if len(attachments) == 0 {
		return message
	}
	message += "\n\nAdditional file context:"
	for _, attachment := range attachments {
		message += fmt.Sprintf("\n\nAttached file: %s\n```\n%s\n```", attachment.Name, attachment.Content)
	}
	return message
}
