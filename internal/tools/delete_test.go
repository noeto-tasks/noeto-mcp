package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"noeto-mcp/internal/noeto"
)

func plantCopy(t *testing.T, api *attachmentAPI, id, uploader, filename string, day int) {
	t.Helper()
	api.plant(t, noeto.Attachment{
		ID: "e0000000-0000-4000-8000-" + id, UploadedByID: uploader, UploadedBy: "někdo",
		Filename: filename, ContentType: "text/plain", SizeBytes: 3,
		Status: noeto.AttachmentReady, CreatedAt: time.Date(2026, 8, day, 9, 0, 0, 0, time.UTC),
	}, []byte("abc"))
}

func remainingIDs(api *attachmentAPI) []string {
	var ids []string
	for _, row := range api.snapshot() {
		ids = append(ids, row.ID)
	}
	return ids
}

func TestDeleteAttachment_RemovesWhatThisAccountUploaded(t *testing.T) {
	s, api := newDocumentServer(t)
	path := localFile(t, "omyl.pdf", []byte("%PDF-1.7\n"))
	attach(t, s, attachFileIn{Card: cardID, Path: path})

	_, view, err := s.deleteAttachment(context.Background(), nil, deleteAttachmentIn{Card: cardID, Filename: "omyl.pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if view.Filename != "omyl.pdf" || view.Note != "" {
		t.Errorf("view = %+v", view)
	}
	if rows := api.snapshot(); len(rows) != 0 {
		t.Errorf("want the card empty, got %+v", rows)
	}
}

// The token in the fake belongs to a manager, whom the API would let delete
// anything. The tool must still refuse somebody else's file.
func TestDeleteAttachment_RefusesSomebodyElsesFileEvenForAManager(t *testing.T) {
	s, api := newDocumentServer(t)
	plantCopy(t, api, "000000000001", themID, "zadani.txt", 20)

	_, _, err := s.deleteAttachment(context.Background(), nil, deleteAttachmentIn{Card: cardID, Filename: "zadani.txt"})
	if err == nil || !strings.Contains(err.Error(), "somebody else") {
		t.Fatalf("want a refusal naming the reason, got %v", err)
	}
	if rows := api.snapshot(); len(rows) != 1 {
		t.Errorf("their file must stay, got %+v", rows)
	}
}

func TestDeleteAttachment_TakesTheNewestOwnCopyAndSaysWhatIsLeft(t *testing.T) {
	s, api := newDocumentServer(t)
	plantCopy(t, api, "000000000001", usID, "log.txt", 20)
	plantCopy(t, api, "000000000002", usID, "log.txt", 22)
	plantCopy(t, api, "000000000003", themID, "log.txt", 23)

	_, view, err := s.deleteAttachment(context.Background(), nil, deleteAttachmentIn{Card: cardID, Filename: "LOG.txt"})
	if err != nil {
		t.Fatal(err)
	}
	ids := strings.Join(remainingIDs(api), " ")
	if strings.Contains(ids, "000000000002") || !strings.Contains(ids, "000000000001") || !strings.Contains(ids, "000000000003") {
		t.Errorf("want only the newest own copy gone, left %s", ids)
	}
	if !strings.Contains(view.Note, "2 copies") {
		t.Errorf("note should say what is left, got %q", view.Note)
	}
}

func TestDeleteAttachment_MissingFileListsWhatIsThere(t *testing.T) {
	s, api := newDocumentServer(t)
	plantCopy(t, api, "000000000001", usID, "report.txt", 20)

	_, _, err := s.deleteAttachment(context.Background(), nil, deleteAttachmentIn{Card: cardID, Filename: "missing.zip"})
	if err == nil || !strings.Contains(err.Error(), "report.txt") {
		t.Errorf("want an error naming what is there, got %v", err)
	}
}

func TestDeleteAttachment_RejectsACardGivenByTitle(t *testing.T) {
	s, _ := newDocumentServer(t)

	if _, _, err := s.deleteAttachment(context.Background(), nil, deleteAttachmentIn{Card: "Maximální počet dětí", Filename: "a.txt"}); err == nil {
		t.Error("want the card id required")
	}
}
