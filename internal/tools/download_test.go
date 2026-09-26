package tools

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newDownloadServer(t *testing.T) (*server, *attachmentAPI) {
	t.Helper()
	s, api := newDocumentServer(t)
	s.downloads = t.TempDir()
	return s, api
}

func download(t *testing.T, s *server, filename string) *downloadView {
	t.Helper()
	_, view, err := s.downloadAttachment(context.Background(), nil, downloadAttachmentIn{Card: cardID, Filename: filename})
	if err != nil {
		t.Fatalf("download %s: %v", filename, err)
	}
	return view
}

// The point of the tool: what read_attachment turns away still reaches the
// agent, byte for byte, as a file it can open with whatever it has.
func TestDownloadAttachment_SavesWhatReadAttachmentRefuses(t *testing.T) {
	s, api := newDownloadServer(t)
	pdf := []byte("%PDF-1.7\n\x00\x01binary\n%%EOF")
	put(t, api, "report.pdf", "application/pdf", pdf)

	view := download(t, s, "report.pdf")

	want := filepath.Join(s.downloads, cardID, "report.pdf")
	if view.Path != want {
		t.Errorf("path = %s, want %s", view.Path, want)
	}
	saved, err := os.ReadFile(view.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(saved, pdf) {
		t.Errorf("saved %q, want %q", saved, pdf)
	}
	if view.Bytes != int64(len(pdf)) || view.Type != "application/pdf" {
		t.Errorf("view = %+v", view)
	}
}

// The filename is whatever the uploader typed, so it must not be able to steer
// the write outside the download directory.
func TestDownloadAttachment_KeepsTheUploadersNameInsideTheDirectory(t *testing.T) {
	s, api := newDownloadServer(t)
	put(t, api, "../../.bashrc", "text/plain", []byte("echo pwned"))

	view := download(t, s, "../../.bashrc")

	want := filepath.Join(s.downloads, cardID, ".bashrc")
	if view.Path != want {
		t.Errorf("path = %s, want %s", view.Path, want)
	}
}

func TestDownloadAttachment_ReplacesAnEarlierCopy(t *testing.T) {
	s, api := newDownloadServer(t)
	put(t, api, "data.csv", "text/csv", []byte("a,b\n"))
	first := download(t, s, "data.csv")

	api.mu.Lock()
	api.rows = nil
	api.mu.Unlock()
	put(t, api, "data.csv", "text/csv", []byte("a,b,c\n"))
	second := download(t, s, "data.csv")

	saved, err := os.ReadFile(second.Path)
	if err != nil {
		t.Fatal(err)
	}
	if first.Path != second.Path || string(saved) != "a,b,c\n" {
		t.Errorf("want one file holding the newer copy, got %s holding %q", second.Path, saved)
	}
	entries, _ := os.ReadDir(filepath.Dir(second.Path))
	if len(entries) != 1 {
		t.Errorf("a temporary file was left behind: %v", entries)
	}
}

func TestDownloadAttachment_NamesWhatIsOnTheCardWhenTheFileIsNot(t *testing.T) {
	s, api := newDownloadServer(t)
	put(t, api, "report.pdf", "application/pdf", []byte("%PDF"))

	_, _, err := s.downloadAttachment(context.Background(), nil, downloadAttachmentIn{Card: cardID, Filename: "missing.zip"})
	if err == nil || !strings.Contains(err.Error(), "report.pdf") {
		t.Errorf("want an error naming what is there, got %v", err)
	}
}

func TestLocalFilename(t *testing.T) {
	for uploaded, want := range map[string]string{
		"notes.txt":           "notes.txt",
		`..\..\evil.exe`:      "evil.exe",
		"/etc/passwd":         "passwd",
		"..":                  "fallback-id",
		"a\u202etxt.exe":      "atxt.exe",
		"line\nbreak.txt":     "linebreak.txt",
		"  spaced name.pdf  ": "spaced name.pdf",
	} {
		if got := localFilename(uploaded, "fallback-id"); got != want {
			t.Errorf("localFilename(%q) = %q, want %q", uploaded, got, want)
		}
	}
}
