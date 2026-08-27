package tools

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"noeto-mcp/internal/noeto"
)

// The fixtures are encoded rather than pasted as base64, so a test that asserts
// "these bytes sniff as a PNG" is asserting about a real PNG.
func encoded(t *testing.T, as string) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 0x33, G: 0x66, B: 0x99, A: 0xff})

	var buf bytes.Buffer
	var err error
	switch as {
	case "png":
		err = png.Encode(&buf, img)
	case "jpeg":
		err = jpeg.Encode(&buf, img, nil)
	case "gif":
		err = gif.Encode(&buf, img, nil)
	default:
		t.Fatalf("no encoder for %q", as)
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// put drops a file on the card as somebody other than this server, which is the
// only way an arbitrary attachment gets there — this server uploads documents
// and nothing else.
func put(t *testing.T, api *attachmentAPI, filename, contentType string, body []byte) {
	t.Helper()
	api.plant(t, noeto.Attachment{
		ID:           "e0000000-0000-4000-8000-" + pad(filename),
		UploadedByID: themID,
		UploadedBy:   "Jana Nováková",
		Filename:     filename,
		ContentType:  contentType,
		SizeBytes:    int64(len(body)),
		Status:       noeto.AttachmentReady,
		CreatedAt:    time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC),
	}, body)
}

// pad derives the last group of a UUID from the filename, so each fixture gets
// a distinct id without the test having to invent one.
func pad(filename string) string {
	var sum uint32 = 2166136261
	for _, b := range []byte(filename) {
		sum = (sum ^ uint32(b)) * 16777619
	}
	return fmt.Sprintf("%012x", sum)
}

func read(t *testing.T, s *server, filename string) (*mcp.CallToolResult, *attachmentView) {
	t.Helper()
	res, view, err := s.readAttachment(context.Background(), nil, readAttachmentIn{Card: cardID, Filename: filename})
	if err != nil {
		t.Fatalf("read %s: %v", filename, err)
	}
	return res, view
}

func refused(t *testing.T, s *server, filename string) string {
	t.Helper()
	_, view, err := s.readAttachment(context.Background(), nil, readAttachmentIn{Card: cardID, Filename: filename})
	if err == nil {
		t.Fatalf("expected %s to be refused, got %+v", filename, view)
	}
	return err.Error()
}

// ── text ────────────────────────────────────────────────────────────────────

func TestReadAttachment_TextComesBackWithItsProvenance(t *testing.T) {
	s, api := newDocumentServer(t)
	const body = "2026-08-20 09:00:01 ERROR  login: 500 on Safari\n2026-08-20 09:00:02 INFO   retrying\n"
	put(t, api, "safari.log", "text/plain; charset=utf-8", []byte(body))

	_, view := read(t, s, "safari.log")

	if view.Text != body {
		t.Errorf("text = %q, want %q", view.Text, body)
	}
	if view.Markdown != "" {
		t.Errorf("a plain log is not a document, got markdown %q", view.Markdown)
	}
	if view.Type != "text/plain" {
		t.Errorf("type = %q, want the parameters dropped", view.Type)
	}
	// Who uploaded it is half of reading it: the contents are somebody's input,
	// and the answer has to say whose.
	if view.UploadedBy != "Jana Nováková" {
		t.Errorf("uploaded_by = %q, want the uploader named", view.UploadedBy)
	}
	if view.When == "" {
		t.Error("when is empty")
	}
	if view.Bytes != int64(len(body)) {
		t.Errorf("bytes = %d, want %d", view.Bytes, len(body))
	}
}

// The declared type is a field the uploader filled in. If it decided anything
// on its own, this is the call that would put a binary into the conversation.
func TestReadAttachment_BinaryCallingItselfTextIsRefused(t *testing.T) {
	s, api := newDocumentServer(t)
	put(t, api, "notes.txt", "text/plain", []byte{0x00, 0xff, 0xfe, 'h', 'i', 0x80})

	if msg := refused(t, s, "notes.txt"); !strings.Contains(msg, "UTF-8") {
		t.Errorf("the refusal should say the bytes did not decode: %s", msg)
	}
}

// An escape sequence in a file somebody uploaded is rendered by whatever
// terminal prints the answer, which is why this is refused and not merely odd.
func TestReadAttachment_TextHoldingAnEscapeSequenceIsRefused(t *testing.T) {
	s, api := newDocumentServer(t)
	put(t, api, "motd.txt", "text/plain", []byte("ahoj \x1b[2J\x1b[1;1H nic tu není"))

	if msg := refused(t, s, "motd.txt"); !strings.Contains(msg, "control character") {
		t.Errorf("the refusal should name the problem: %s", msg)
	}
}

func TestReadAttachment_TabsAndNewlinesAreOrdinaryText(t *testing.T) {
	s, api := newDocumentServer(t)
	const body = "jméno\tvěk\r\nAnna\t31\r\n"
	put(t, api, "lidi.tsv", "text/tab-separated-values", []byte(body))

	if _, view := read(t, s, "lidi.tsv"); view.Text != body {
		t.Errorf("text = %q, want %q", view.Text, body)
	}
}

// ── images ──────────────────────────────────────────────────────────────────

func TestReadAttachment_ImageComesBackAsAnImageBlock(t *testing.T) {
	s, api := newDocumentServer(t)
	body := encoded(t, "png")
	put(t, api, "chyba.png", "image/png", body)

	res, view := read(t, s, "chyba.png")

	if res == nil || len(res.Content) != 1 {
		t.Fatalf("want one content block, got %+v", res)
	}
	img, ok := res.Content[0].(*mcp.ImageContent)
	if !ok {
		t.Fatalf("content is %T, want an image block", res.Content[0])
	}
	if !bytes.Equal(img.Data, body) {
		t.Error("the image block does not carry the object's bytes")
	}
	if img.MIMEType != "image/png" {
		t.Errorf("mime = %q, want image/png", img.MIMEType)
	}
	// The pixels travel in the content block; the structured half describes
	// them and must not carry a second copy as text.
	if view.Text != "" || view.Markdown != "" {
		t.Errorf("an image must not also come back as text: %+v", view)
	}
}

func TestReadAttachment_BinaryCallingItselfAnImageIsRefused(t *testing.T) {
	s, api := newDocumentServer(t)
	put(t, api, "sken.png", "image/png", []byte("PK\x03\x04 tohle je zip"))

	if msg := refused(t, s, "sken.png"); !strings.Contains(msg, "not an image") {
		t.Errorf("the refusal should say what the bytes are: %s", msg)
	}
}

// A mislabelled JPEG is still a JPEG. Refusing it would be pedantry; sending it
// under the label the card carries would be a decode failure further from the
// cause than here.
func TestReadAttachment_MislabelledImageIsSentUnderItsRealType(t *testing.T) {
	s, api := newDocumentServer(t)
	put(t, api, "screenshot.jpg", "image/jpeg", encoded(t, "gif"))

	res, view := read(t, s, "screenshot.jpg")

	img := res.Content[0].(*mcp.ImageContent)
	if img.MIMEType != "image/gif" {
		t.Errorf("mime = %q, want the sniffed type", img.MIMEType)
	}
	if !strings.Contains(view.Note, "image/jpeg") || !strings.Contains(view.Note, "image/gif") {
		t.Errorf("note should name both labels, got %q", view.Note)
	}
}

// SVG is markup that can carry script, and nothing decodes it on the way past.
// Its source is both the honest answer and the safe one.
func TestReadAttachment_SVGIsReadAsSourceNotAsAnImage(t *testing.T) {
	s, api := newDocumentServer(t)
	const body = `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`
	put(t, api, "ikona.svg", "image/svg+xml", []byte(body))

	res, view := read(t, s, "ikona.svg")

	if res != nil {
		t.Fatalf("SVG must not come back as an image block: %+v", res)
	}
	if view.Text != body {
		t.Errorf("text = %q, want the source verbatim", view.Text)
	}
}

// ── what cannot be handed over ──────────────────────────────────────────────

func TestReadAttachment_PDFIsRefusedByName(t *testing.T) {
	s, api := newDocumentServer(t)
	put(t, api, "zadani.pdf", "application/pdf", []byte("%PDF-1.7\n"))

	msg := refused(t, s, "zadani.pdf")
	for _, want := range []string{"zadani.pdf", "application/pdf", "browser"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal should mention %q: %s", want, msg)
		}
	}
}

// The size the API signed into the presigned PUT is the object's real length,
// so the refusal happens before anything is fetched. The body here is tiny: if
// the check ran after the download this would succeed.
func TestReadAttachment_OversizeIsRefusedBeforeTheDownload(t *testing.T) {
	s, api := newDocumentServer(t)
	api.plant(t, noeto.Attachment{
		ID: "e0000000-0000-4000-8000-000000000abc", UploadedByID: themID, UploadedBy: "Jana Nováková",
		Filename: "dump.txt", ContentType: "text/plain", SizeBytes: maxTextBytes + 1,
		Status: noeto.AttachmentReady, CreatedAt: time.Now(),
	}, []byte("malé"))

	if msg := refused(t, s, "dump.txt"); !strings.Contains(msg, "browser") {
		t.Errorf("the refusal should say what to do instead: %s", msg)
	}
}

// ── agreement with read_document ────────────────────────────────────────────

// The two tools must not give different answers for the same file. A design
// document read through the general tool is still the document.
func TestReadAttachment_ADocumentComesBackAsItsSource(t *testing.T) {
	s, _ := newDocumentServer(t)
	ctx := context.Background()

	if _, _, err := s.attachDocument(ctx, nil, attachDocumentIn{Card: cardID, Markdown: sampleMarkdown}); err != nil {
		t.Fatal(err)
	}

	_, view := read(t, s, defaultDocumentName)
	if view.Markdown != sampleMarkdown {
		t.Errorf("markdown = %q, want the source read_document would give", view.Markdown)
	}
	if view.Text != "" {
		t.Error("a sealed document must not also come back as raw HTML")
	}
}

// HTML somebody uploaded by hand carries no source block, and read_document
// refuses it. Here it is ordinary text, which is the point of having both.
func TestReadAttachment_HandWrittenHTMLIsReadAsText(t *testing.T) {
	s, api := newDocumentServer(t)
	const body = "<h1>Poznámky</h1>\n<p>Nic zvláštního.</p>\n"
	put(t, api, "poznamky.html", "text/html", []byte(body))

	_, view := read(t, s, "poznamky.html")
	if view.Text != body {
		t.Errorf("text = %q, want the file verbatim", view.Text)
	}
	if view.Markdown != "" {
		t.Errorf("there is no source block to extract, got %q", view.Markdown)
	}
}

// ── addressing ──────────────────────────────────────────────────────────────

func TestReadAttachment_MissingFileListsWhatIsThere(t *testing.T) {
	s, api := newDocumentServer(t)
	put(t, api, "zadani.pdf", "application/pdf", []byte("%PDF-1.7\n"))

	msg := refused(t, s, "graf.png")
	if !strings.Contains(msg, "zadani.pdf") {
		t.Errorf("the error should name what the card does hold: %s", msg)
	}
}

func TestReadAttachment_EmptyFilenameSaysWhereToGetOne(t *testing.T) {
	s, _ := newDocumentServer(t)

	_, _, err := s.readAttachment(context.Background(), nil, readAttachmentIn{Card: cardID, Filename: "  "})
	if err == nil {
		t.Fatal("expected an empty filename to be rejected")
	}
	if !strings.Contains(err.Error(), "get_card") {
		t.Errorf("the error should say where the name comes from: %v", err)
	}
}

// documentFilename's rules are about a name this server writes. A name somebody
// else chose only has to match, and applying the write rules would make a real
// attachment unreadable.
func TestReadAttachment_ReadsANameAttachDocumentWouldNotWrite(t *testing.T) {
	s, api := newDocumentServer(t)
	put(t, api, "graf: srpen.csv", "text/csv", []byte("den,počet\n1,4\n"))

	if _, view := read(t, s, "graf: srpen.csv"); !strings.Contains(view.Text, "den,počet") {
		t.Errorf("text = %q", view.Text)
	}
}

func TestReadAttachment_DuplicateNamesSayAChoiceWasMade(t *testing.T) {
	s, api := newDocumentServer(t)
	for i, body := range []string{"starý\n", "nový\n"} {
		api.plant(t, noeto.Attachment{
			ID: "e0000000-0000-4000-8000-00000000000" + string(rune('1'+i)), UploadedByID: themID, UploadedBy: "Jana Nováková",
			Filename: "log.txt", ContentType: "text/plain", SizeBytes: int64(len(body)),
			Status: noeto.AttachmentReady, CreatedAt: time.Date(2026, 8, 20+i, 9, 0, 0, 0, time.UTC),
		}, []byte(body))
	}

	_, view := read(t, s, "log.txt")
	if view.Text != "nový\n" {
		t.Errorf("text = %q, want the newest copy", view.Text)
	}
	if !strings.Contains(view.Note, "2 copies") {
		t.Errorf("note should say the card holds more than one, got %q", view.Note)
	}
}

// ── get_card lists what read_attachment can be asked for ────────────────────

func TestGetCard_NamesTheFilesOnTheCard(t *testing.T) {
	s, api := newDocumentServer(t)
	put(t, api, "zadani.pdf", "application/pdf", []byte("%PDF-1.7\n"))
	put(t, api, "chyba.png", "image/png", encoded(t, "png"))

	_, view, err := s.getCard(context.Background(), nil, cardIn{Card: cardID})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Files) != 2 {
		t.Fatalf("files = %+v, want both", view.Files)
	}
	if view.Files[0].Filename != "chyba.png" || view.Files[1].Filename != "zadani.pdf" {
		t.Errorf("files should be in name order, got %+v", view.Files)
	}
	if view.Files[0].Type != "image/png" || view.Files[0].Bytes == 0 {
		t.Errorf("a listed file should say what it is and how big, got %+v", view.Files[0])
	}
	if view.Note != "" {
		t.Errorf("unexpected note: %q", view.Note)
	}
}

// A card with no files must not pay for a second request, and must not come
// back with an empty list where the field would read as meaningful.
func TestGetCard_NoFilesMeansNoListing(t *testing.T) {
	s, _ := newDocumentServer(t)

	_, view, err := s.getCard(context.Background(), nil, cardIn{Card: cardID})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Files) != 0 {
		t.Errorf("files = %+v, want none", view.Files)
	}
}

// The card is what get_card is for. Losing the whole answer because the file
// listing failed would be the wrong way round — but so would a silently empty
// list, which reads as a card with nothing attached.
func TestGetCard_ListingFailureIsReportedNotFatal(t *testing.T) {
	s, api := newDocumentServer(t)
	put(t, api, "zadani.pdf", "application/pdf", []byte("%PDF-1.7\n"))
	api.mu.Lock()
	api.listStatus = 500
	api.mu.Unlock()

	_, view, err := s.getCard(context.Background(), nil, cardIn{Card: cardID})
	if err != nil {
		t.Fatalf("get_card should survive a failed listing: %v", err)
	}
	if view.Title == "" {
		t.Error("the card itself should still be there")
	}
	if !strings.Contains(view.Note, "could not be listed") {
		t.Errorf("note should say the listing failed, got %q", view.Note)
	}
}
