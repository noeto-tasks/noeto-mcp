package tools

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func localFile(t *testing.T, name string, body []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func attach(t *testing.T, s *server, in attachFileIn) *uploadView {
	t.Helper()
	_, view, err := s.attachFile(context.Background(), nil, in)
	if err != nil {
		t.Fatalf("attach %s: %v", in.Path, err)
	}
	return view
}

func TestAttachFile_AnyTypeGoesUpByteForByte(t *testing.T) {
	s, api := newDocumentServer(t)
	pdf := []byte("%PDF-1.7\n\x00\x01binary\n%%EOF")
	path := localFile(t, "zadani.pdf", pdf)

	view := attach(t, s, attachFileIn{Card: cardID, Path: path})

	if view.Filename != "zadani.pdf" || view.Type != "application/pdf" || view.Bytes != int64(len(pdf)) {
		t.Errorf("view = %+v", view)
	}
	if stored := api.object("/objects/f0000000-0000-4000-8000-000000000001"); !bytes.Equal(stored, pdf) {
		t.Errorf("stored %q, want %q", stored, pdf)
	}
}

// What attach_file puts up, read_attachment must get back — the two are the
// halves of moving a file through the card.
func TestAttachFile_RoundTripsThroughReadAttachment(t *testing.T) {
	s, _ := newDocumentServer(t)
	png := encoded(t, "png")
	path := localFile(t, "screenshot.png", png)

	attach(t, s, attachFileIn{Card: cardID, Path: path, Filename: "chyba.png"})

	res, _ := read(t, s, "chyba.png")
	if res == nil || len(res.Content) != 1 {
		t.Fatalf("want one image block, got %+v", res)
	}
}

func TestAttachFile_AddsBesideWhatIsThereRatherThanReplacing(t *testing.T) {
	s, api := newDocumentServer(t)
	put(t, api, "log.txt", "text/plain", []byte("starý\n"))

	attach(t, s, attachFileIn{Card: cardID, Path: localFile(t, "log.txt", []byte("nový\n"))})

	if rows := api.snapshot(); len(rows) != 2 {
		t.Errorf("want both copies on the card, got %d", len(rows))
	}
}

// An extension nobody registered still needs a type the API will take; the
// bytes decide.
func TestAttachFile_SniffsWhenTheExtensionNamesNothing(t *testing.T) {
	s, _ := newDocumentServer(t)

	view := attach(t, s, attachFileIn{Card: cardID, Path: localFile(t, "trace.noetolog", []byte("plain words\n"))})

	if view.Type != "text/plain" {
		t.Errorf("type = %q, want it sniffed from the bytes", view.Type)
	}
}

func TestAttachFile_RejectsWhatItCannotSendWhole(t *testing.T) {
	s, _ := newDocumentServer(t)
	dir := t.TempDir()

	for name, in := range map[string]attachFileIn{
		"relative path":  {Card: cardID, Path: "notes.txt"},
		"directory":      {Card: cardID, Path: dir},
		"missing file":   {Card: cardID, Path: filepath.Join(dir, "nothing.txt")},
		"empty file":     {Card: cardID, Path: localFile(t, "empty.txt", nil)},
		"card by title":  {Card: "Maximální počet dětí", Path: localFile(t, "a.txt", []byte("a"))},
		"path in name":   {Card: cardID, Path: localFile(t, "b.txt", []byte("b")), Filename: "../b.txt"},
		"bidi in name":   {Card: cardID, Path: localFile(t, "c.txt", []byte("c")), Filename: "c\u202etxt.exe"},
		"colon in name":  {Card: cardID, Path: localFile(t, "d.txt", []byte("d")), Filename: "d: final.txt"},
		"newline in arg": {Card: cardID, Path: localFile(t, "e.txt", []byte("e")), Filename: "e\n.txt"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, view, err := s.attachFile(context.Background(), nil, in); err == nil {
				t.Errorf("want a rejection, got %+v", view)
			}
		})
	}
}

func TestAttachFile_ARejectedPathSaysWhatWasWrong(t *testing.T) {
	s, _ := newDocumentServer(t)

	_, _, err := s.attachFile(context.Background(), nil, attachFileIn{Card: cardID, Path: "notes.txt"})
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("want the error to ask for an absolute path, got %v", err)
	}
}
