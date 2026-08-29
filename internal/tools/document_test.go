package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"noeto-mcp/internal/noeto"
)

// fakeStore stands in for the object store the presigned URLs point at.
//
// It is a second server rather than another route on the fake API, because the
// thing most worth asserting about the upload is that it goes somewhere else —
// and that the noeto access token does not go with it.
type fakeStore struct {
	*httptest.Server

	mu      sync.Mutex
	objects map[string][]byte
	// auth records the Authorization header of every request that reached the
	// store. It must stay empty: the presigned URL carries its own
	// authorization and the noeto token has no business on this host.
	auth []string
	// putStatus, when set, is what the store answers a PUT with, to exercise
	// the failure path.
	putStatus int
}

func newFakeStore(t *testing.T) *fakeStore {
	t.Helper()
	store := &fakeStore{objects: map[string][]byte{}}

	store.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		defer store.mu.Unlock()

		if header := r.Header.Get("Authorization"); header != "" {
			store.auth = append(store.auth, header)
		}

		switch r.Method {
		case http.MethodPut:
			if store.putStatus != 0 {
				w.WriteHeader(store.putStatus)
				return
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			store.objects[r.URL.Path] = body
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			object, ok := store.objects[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(object)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(store.Close)
	return store
}

// attachmentAPI is the fake API extended with the attachment endpoints, holding
// the rows in memory so a replace can actually be observed.
type attachmentAPI struct {
	*httptest.Server
	store *fakeStore

	mu   sync.Mutex
	rows []noeto.Attachment
	seq  int
	// completeStatus, when set, is what /complete answers, to exercise the
	// cleanup path.
	completeStatus int
	// deleteStatus, when set, is what DELETE answers, to exercise the branch
	// where the new document lands but a stale copy survives.
	deleteStatus int
	// hideUploader blanks uploaded_by_user_id, standing in for the API dropping
	// or renaming the field the replace safety check depends on.
	hideUploader bool
	// uploadURL, when set, overrides where the client is told to PUT.
	uploadURL string
	// listStatus, when set, is what the attachment listing answers, to exercise
	// get_card degrading rather than failing outright.
	listStatus int
}

const (
	usID   = "d0000000-0000-4000-8000-00000000000a"
	themID = "d0000000-0000-4000-8000-00000000000b"
)

func newAttachmentAPI(t *testing.T) *attachmentAPI {
	t.Helper()
	api := &attachmentAPI{store: newFakeStore(t)}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /boards/{id}", serveBoard)
	mux.HandleFunc("GET /members", serveMembers)

	mux.HandleFunc("GET /cards/{id}/detail", func(w http.ResponseWriter, _ *http.Request) {
		api.mu.Lock()
		count := len(api.ready())
		api.mu.Unlock()
		writeJSON(w, map[string]any{
			"card": map[string]any{
				"id": cardID, "board_id": boardID, "column_id": todoID,
				"title": "Maximální počet dětí u rodiče", "label_ids": []string{}, "rank": "b",
				"attachment_count": count,
			},
			"comments": []any{},
		})
	})

	mux.HandleFunc("GET /cards/{id}/attachments", func(w http.ResponseWriter, _ *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		if api.listStatus != 0 {
			w.WriteHeader(api.listStatus)
			writeJSON(w, map[string]any{"status": api.listStatus, "code": "internal", "detail": "The listing failed."})
			return
		}
		writeJSON(w, map[string]any{"attachments": api.ready()})
	})

	mux.HandleFunc("POST /cards/{id}/attachments", func(w http.ResponseWriter, r *http.Request) {
		var body noeto.NewAttachment
		_ = json.NewDecoder(r.Body).Decode(&body)

		api.mu.Lock()
		defer api.mu.Unlock()
		api.seq++
		id := fmt.Sprintf("f0000000-0000-4000-8000-%012d", api.seq)
		key := "/objects/" + id

		api.rows = append(api.rows, noeto.Attachment{
			ID: id, UploadedByID: usID, UploadedBy: "Michal Bocek",
			Filename: body.Filename, ContentType: body.ContentType, SizeBytes: body.SizeBytes,
			Status: "pending", CreatedAt: time.Now(),
			DownloadURL: api.store.URL + key,
		})

		uploadURL := api.store.URL + key
		if api.uploadURL != "" {
			uploadURL = api.uploadURL
		}

		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{
			"attachment":     api.present(api.rows[len(api.rows)-1]),
			"upload_url":     uploadURL,
			"upload_headers": map[string]string{"Content-Type": body.ContentType},
		})
	})

	mux.HandleFunc("POST /attachments/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		if api.completeStatus != 0 {
			w.WriteHeader(api.completeStatus)
			writeJSON(w, map[string]any{"status": api.completeStatus, "code": "upload_incomplete", "detail": "The upload did not land."})
			return
		}
		row := api.find(r.PathValue("id"))
		if row == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		row.Status = noeto.AttachmentReady
		writeJSON(w, map[string]any{"attachment": api.present(*row)})
	})

	mux.HandleFunc("DELETE /attachments/{id}", func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		if api.deleteStatus != 0 {
			w.WriteHeader(api.deleteStatus)
			writeJSON(w, map[string]any{"status": api.deleteStatus, "code": "internal", "detail": "Something went wrong."})
			return
		}
		id := r.PathValue("id")
		for i, row := range api.rows {
			if row.ID != id {
				continue
			}
			// The API lets an uploader delete their own; anything else is a 404.
			if row.UploadedByID != usID {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			api.rows = append(api.rows[:i], api.rows[i+1:]...)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	api.Server = httptest.NewServer(mux)
	t.Cleanup(api.Close)
	return api
}

// ready mirrors the API, which hides pending rows because their object may not
// exist yet.
func (a *attachmentAPI) ready() []noeto.Attachment {
	out := make([]noeto.Attachment, 0, len(a.rows))
	for _, row := range a.rows {
		if row.Status == noeto.AttachmentReady {
			out = append(out, a.present(row))
		}
	}
	return out
}

// present is the wire shape, which is where hideUploader takes effect.
func (a *attachmentAPI) present(row noeto.Attachment) noeto.Attachment {
	if a.hideUploader {
		row.UploadedByID = ""
	}
	return row
}

func (a *attachmentAPI) find(id string) *noeto.Attachment {
	for i := range a.rows {
		if a.rows[i].ID == id {
			return &a.rows[i]
		}
	}
	return nil
}

// object reads a stored body under the store's own lock, which the handler
// holds when writing it.
func (a *attachmentAPI) object(path string) []byte {
	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	return a.store.objects[path]
}

// plant puts an attachment on the card that this tool did not upload — the
// fixture for "somebody else's file" and for "a design.md put here by hand".
// The row's download URL is wired to the body so read_document has something
// real to fetch.
func (a *attachmentAPI) plant(t *testing.T, row noeto.Attachment, body []byte) {
	t.Helper()
	key := "/objects/planted-" + row.ID

	a.store.mu.Lock()
	a.store.objects[key] = body
	a.store.mu.Unlock()

	row.DownloadURL = a.store.URL + key
	a.mu.Lock()
	a.rows = append(a.rows, row)
	a.mu.Unlock()
}

func (a *attachmentAPI) snapshot() []noeto.Attachment {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]noeto.Attachment(nil), a.rows...)
}

func newDocumentServer(t *testing.T) (*server, *attachmentAPI) {
	t.Helper()
	api := newAttachmentAPI(t)
	return &server{api: noeto.New(api.URL, "noeto_pat_test")}, api
}

const sampleMarkdown = `# Limit dětí u rodiče

## Rozhodnutí

Limit je **konfigurovatelný**, ne zadrátovaný.

| Varianta | Proč ne |
|---|---|
| pevná 10 | sdílená péče |

` + "```go\nif n > limit { return err }\n```" + `

Viz <https://example.com> & "uvozovky".
`

// ── round trip ──────────────────────────────────────────────────────────────

// The pair only earns its cost if the source survives the round trip exactly.
// A document that comes back subtly different is worse than none: the next pass
// would rewrite it from a corrupted base.
func TestDocument_SourceSurvivesTheRoundTrip(t *testing.T) {
	s, api := newDocumentServer(t)
	ctx := context.Background()

	if _, _, err := s.attachDocument(ctx, nil, attachDocumentIn{Card: cardID, Markdown: sampleMarkdown}); err != nil {
		t.Fatal(err)
	}

	_, back, err := s.readDocument(ctx, nil, readDocumentIn{Card: cardID})
	if err != nil {
		t.Fatal(err)
	}
	if back.Markdown != sampleMarkdown {
		t.Fatalf("source did not round trip:\n got: %q\nwant: %q", back.Markdown, sampleMarkdown)
	}
	if back.Filename != defaultDocumentName {
		t.Errorf("filename = %q, want %q", back.Filename, defaultDocumentName)
	}

	// And the file is the Markdown, not a rendering of it with the Markdown
	// tucked away somewhere inside. That is the whole reason the format changed,
	// so it is asserted on the bytes in the store rather than inferred from the
	// round trip above.
	stored := api.object("/objects/f0000000-0000-4000-8000-000000000001")
	if string(stored) != sampleMarkdown {
		t.Errorf("the stored object is not the document:\n got: %q\nwant: %q", stored, sampleMarkdown)
	}
}

// Markdown is stored as it was written, so markup inside it is neither escaped
// nor stripped on the way there and back. The HTML format this replaced had to
// seal the source away from its own rendering to manage that; a .md has nothing
// to seal it from.
func TestDocument_MarkupInTheSourceIsStoredVerbatim(t *testing.T) {
	source := "Nepoužívat inline skripty:\n\n```html\n</script><script>alert(1)</script>\n```\n\n" +
		"Ampersand &amp; a &lt;tag&gt; a skutečné < a &.\n"

	s, api := newDocumentServer(t)
	ctx := context.Background()

	if _, _, err := s.attachDocument(ctx, nil, attachDocumentIn{Card: cardID, Markdown: source}); err != nil {
		t.Fatal(err)
	}

	if stored := string(api.object("/objects/f0000000-0000-4000-8000-000000000001")); stored != source {
		t.Errorf("the stored object is not the document:\n got: %q\nwant: %q", stored, source)
	}

	_, back, err := s.readDocument(ctx, nil, readDocumentIn{Card: cardID})
	if err != nil {
		t.Fatal(err)
	}
	if back.Markdown != source {
		t.Fatalf("source did not round trip:\n got: %q\nwant: %q", back.Markdown, source)
	}
}

// ── replace semantics ───────────────────────────────────────────────────────

// Upload, complete, and only then delete. The card must never be left without a
// design document, which is the whole reason the order is this way round.
func TestAttachDocument_ReplacesTheOldCopyAfterTheNewOneLands(t *testing.T) {
	s, api := newDocumentServer(t)
	ctx := context.Background()

	if _, _, err := s.attachDocument(ctx, nil, attachDocumentIn{Card: cardID, Markdown: "# První\n"}); err != nil {
		t.Fatal(err)
	}
	_, view, err := s.attachDocument(ctx, nil, attachDocumentIn{Card: cardID, Markdown: "# Druhý\n"})
	if err != nil {
		t.Fatal(err)
	}
	if view.Replaced != 1 {
		t.Errorf("replaced = %d, want 1", view.Replaced)
	}

	rows := api.snapshot()
	if len(rows) != 1 {
		t.Fatalf("card holds %d attachments, want exactly the new one: %+v", len(rows), rows)
	}

	_, back, err := s.readDocument(ctx, nil, readDocumentIn{Card: cardID})
	if err != nil {
		t.Fatal(err)
	}
	if back.Markdown != "# Druhý\n" {
		t.Errorf("read back %q, want the second document", back.Markdown)
	}
}

// Somebody else's file of the same name is their work. Deleting it would be
// this tool destroying something nobody asked it to touch — and the API would
// refuse anyway, which would fail a call whose real job already succeeded.
func TestAttachDocument_LeavesSomebodyElsesFileAlone(t *testing.T) {
	s, api := newDocumentServer(t)
	ctx := context.Background()

	api.mu.Lock()
	api.rows = append(api.rows, noeto.Attachment{
		ID: "e0000000-0000-4000-8000-000000000001", UploadedByID: themID, UploadedBy: "Jana Nováková",
		Filename: defaultDocumentName, Status: noeto.AttachmentReady, CreatedAt: time.Now().Add(-time.Hour),
		DownloadURL: api.store.URL + "/objects/theirs",
	})
	api.mu.Unlock()

	_, view, err := s.attachDocument(ctx, nil, attachDocumentIn{Card: cardID, Markdown: "# Náš\n"})
	if err != nil {
		t.Fatal(err)
	}
	if view.Replaced != 0 {
		t.Errorf("replaced = %d, want 0 — nothing of ours was there", view.Replaced)
	}
	if view.Note == "" {
		t.Error("a duplicate left on the card has to be reported, or nobody knows to clean it up")
	}

	rows := api.snapshot()
	if len(rows) != 2 {
		t.Fatalf("card holds %d attachments, want theirs and ours: %+v", len(rows), rows)
	}
}

// A failed upload must not leave the pending row it reserved. Nothing reaps
// those on its own, and the card would carry an invisible dead reservation.
func TestAttachDocument_CleansUpAfterAFailedUpload(t *testing.T) {
	s, api := newDocumentServer(t)

	api.store.mu.Lock()
	api.store.putStatus = http.StatusForbidden
	api.store.mu.Unlock()

	_, _, err := s.attachDocument(context.Background(), nil, attachDocumentIn{Card: cardID, Markdown: "# X\n"})
	if err == nil {
		t.Fatal("expected the rejected upload to surface as an error")
	}
	if rows := api.snapshot(); len(rows) != 0 {
		t.Errorf("a pending row was left behind: %+v", rows)
	}
}

func TestAttachDocument_CleansUpAfterAFailedCompletion(t *testing.T) {
	s, api := newDocumentServer(t)

	api.mu.Lock()
	api.completeStatus = http.StatusBadRequest
	api.mu.Unlock()

	_, _, err := s.attachDocument(context.Background(), nil, attachDocumentIn{Card: cardID, Markdown: "# X\n"})
	if err == nil {
		t.Fatal("expected the failed completion to surface as an error")
	}
	if rows := api.snapshot(); len(rows) != 0 {
		t.Errorf("a pending row was left behind: %+v", rows)
	}
}

// The presigned URL is its own authorization and the store is a different host.
// Sending the noeto token there would hand a team-wide credential to a third
// party for no reason at all.
func TestAttachDocument_DoesNotSendTheTokenToTheObjectStore(t *testing.T) {
	s, api := newDocumentServer(t)
	ctx := context.Background()

	if _, _, err := s.attachDocument(ctx, nil, attachDocumentIn{Card: cardID, Markdown: "# X\n"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.readDocument(ctx, nil, readDocumentIn{Card: cardID}); err != nil {
		t.Fatal(err)
	}

	api.store.mu.Lock()
	defer api.store.mu.Unlock()
	if len(api.store.auth) != 0 {
		t.Errorf("the access token reached the object store: %v", api.store.auth)
	}
}

// ── input validation ────────────────────────────────────────────────────────

func TestDocumentFilename(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"", defaultDocumentName, true},
		{"  ", defaultDocumentName, true},
		{"design.md", "design.md", true},
		{"navrh-2026-08-18.md", "navrh-2026-08-18.md", true},
		{"DESIGN.MD", "DESIGN.MD", true},
		{"design.html", "", false},
		{"design", "", false},
		{"../design.md", "", false},
		{"docs/design.md", "", false},
		{`docs\design.md`, "", false},
		{"design:v2.md", "", false},
		{"design\n.md", "", false},
		{"design\u202Edm.md", "", false}, // right-to-left override reorders how it reads
		{"design\u200F.md", "", false},
		{strings.Repeat("a", 260) + ".md", "", false},
	} {
		got, err := documentFilename(tc.in)
		if tc.ok && err != nil {
			t.Errorf("documentFilename(%q) errored: %v", tc.in, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("documentFilename(%q) = %q, want an error", tc.in, got)
		}
		if tc.ok && got != tc.want {
			t.Errorf("documentFilename(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAttachDocument_RejectsACardGivenByTitle(t *testing.T) {
	s, _ := newDocumentServer(t)
	if _, _, err := s.attachDocument(context.Background(), nil,
		attachDocumentIn{Card: "Maximální počet dětí", Markdown: "# X\n"}); err == nil {
		t.Fatal("a card named rather than identified must be rejected")
	}
}

func TestAttachDocument_RejectsAnEmptyDocument(t *testing.T) {
	s, api := newDocumentServer(t)
	if _, _, err := s.attachDocument(context.Background(), nil,
		attachDocumentIn{Card: cardID, Markdown: "   \n"}); err == nil {
		t.Fatal("an empty document must be rejected")
	}
	if rows := api.snapshot(); len(rows) != 0 {
		t.Errorf("something was uploaded despite the rejection: %+v", rows)
	}
}

func TestReadDocument_MissingFileListsWhatIsThere(t *testing.T) {
	s, api := newDocumentServer(t)

	api.mu.Lock()
	api.rows = append(api.rows, noeto.Attachment{
		ID: "e0000000-0000-4000-8000-000000000002", UploadedByID: themID, UploadedBy: "Jana Nováková",
		Filename: "zadani.pdf", Status: noeto.AttachmentReady, CreatedAt: time.Now(),
	})
	api.mu.Unlock()

	_, _, err := s.readDocument(context.Background(), nil, readDocumentIn{Card: cardID})
	if err == nil {
		t.Fatal("expected a missing design document to be reported")
	}
	if !strings.Contains(err.Error(), "zadani.pdf") {
		t.Errorf("the error should name what the card does hold: %v", err)
	}
}

func TestLatestNamed_PrefersTheNewestReadyCopy(t *testing.T) {
	base := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	list := []noeto.Attachment{
		{ID: "old", Filename: "design.md", Status: noeto.AttachmentReady, CreatedAt: base},
		{ID: "pending", Filename: "design.md", Status: "pending", CreatedAt: base.Add(2 * time.Hour)},
		{ID: "new", Filename: "Design.MD", Status: noeto.AttachmentReady, CreatedAt: base.Add(time.Hour)},
	}

	found, candidates := latestNamed(list, "design.md")
	if found == nil || found.ID != "new" {
		t.Fatalf("got %+v, want the newest ready copy regardless of case", found)
	}
	if candidates != 2 {
		t.Errorf("candidates = %d, want the two ready copies (the pending one does not count)", candidates)
	}
}

// ── size ────────────────────────────────────────────────────────────────────

// realisticDocument is the size these documents actually reach — the brainstorm
// artifact this workflow was designed from is fifteen kilobytes of exactly this
// shape. Toy inputs proved nothing about the escaping across a large body, and
// they hid a fake that silently truncated anything past its read buffer.
func realisticDocument() string {
	var b strings.Builder
	b.WriteString("# Propojení karty s implementací\n\n")
	for i := range 60 {
		fmt.Fprintf(&b, "## Oddíl %d\n\nOdstavec s **důrazem**, `kódem` a odkazem na <https://example.com/%d>.\n"+
			"Text pokračuje dál, aby dokument měl reálnou velikost a ne jen pár set bajtů.\n\n"+
			"| Fakt %d | Zdroj |\n|---|---|\n| Hodnota s & a < a > | `soubor.go:%d` |\n\n"+
			"```go\nfunc handler%d(w http.ResponseWriter, r *http.Request) {\n\tif n > limit {\n\t\treturn\n\t}\n}\n```\n\n"+
			"> Citace s uvozovkami \"takhle\" a apostrofem 'takhle'.\n\n"+
			"- položka jedna\n- položka dvě\n  - vnořená\n\n", i, i, i, i*7, i)
	}
	return b.String()
}

func TestDocument_SourceSurvivesARealisticDocument(t *testing.T) {
	s, api := newDocumentServer(t)
	ctx := context.Background()

	source := realisticDocument()
	if len(source) < 20_000 {
		t.Fatalf("the fixture is only %d bytes; it is meant to be a realistic document", len(source))
	}

	if _, _, err := s.attachDocument(ctx, nil, attachDocumentIn{Card: cardID, Markdown: source}); err != nil {
		t.Fatal(err)
	}

	// The stored object has to be the whole document, not whatever fitted in a
	// buffer. Asserted on the tail rather than the length: a fake that
	// pre-allocates ContentLength bytes and fills only the first read leaves a
	// full-length, zero-padded body, so a length check passes and only the end
	// of the file is missing. This fails against both, and fails as a
	// truncation rather than as a document that merely reads oddly.
	stored := api.object("/objects/f0000000-0000-4000-8000-000000000001")
	if !bytes.HasSuffix(stored, []byte("- položka jedna\n- položka dvě\n  - vnořená\n\n")) {
		t.Fatalf("the stored document does not end where it should — the upload was truncated (%d bytes for a %d-byte source)",
			len(stored), len(source))
	}

	_, back, err := s.readDocument(ctx, nil, readDocumentIn{Card: cardID})
	if err != nil {
		t.Fatal(err)
	}
	if back.Markdown != source {
		t.Fatalf("a %d-byte document did not round trip (got %d bytes back)", len(source), len(back.Markdown))
	}
}

func TestAttachDocument_RefusesAnOversizedDocument(t *testing.T) {
	s, api := newDocumentServer(t)

	_, _, err := s.attachDocument(context.Background(), nil,
		attachDocumentIn{Card: cardID, Markdown: strings.Repeat("a", maxSourceBytes+1)})
	if err == nil {
		t.Fatal("expected an oversized document to be refused")
	}
	if rows := api.snapshot(); len(rows) != 0 {
		t.Errorf("something was uploaded despite the refusal: %+v", rows)
	}
}

// Refused, never truncated. A model handed a silently shortened source would
// write the next version from it and lose the rest of the document for good.
func TestReadDocument_RefusesAnOversizedSourceRatherThanTruncating(t *testing.T) {
	s, api := newDocumentServer(t)

	huge := []byte(strings.Repeat("a", maxSourceBytes+1))
	api.plant(t, noeto.Attachment{
		ID: "e0000000-0000-4000-8000-000000000009", UploadedByID: usID, UploadedBy: "Michal Bocek",
		Filename: defaultDocumentName, Status: noeto.AttachmentReady, CreatedAt: time.Now(),
		SizeBytes: int64(len(huge)),
	}, huge)

	_, view, err := s.readDocument(context.Background(), nil, readDocumentIn{Card: cardID})
	if err == nil {
		t.Fatalf("expected an oversized source to be refused, got %d bytes back", len(view.Markdown))
	}
	if !strings.Contains(err.Error(), "browser") {
		t.Errorf("the error should say what to do instead: %v", err)
	}
}

// ── the replace safety checks ───────────────────────────────────────────────

// The uploader comparison is the whole "only our own" property. If the API ever
// stops sending the field, both sides decode to "" and a naive comparison would
// say "ours" for everything on the card.
func TestAttachDocument_RefusesToDeleteWhenTheUploaderIsUnknown(t *testing.T) {
	s, api := newDocumentServer(t)
	ctx := context.Background()

	if _, _, err := s.attachDocument(ctx, nil, attachDocumentIn{Card: cardID, Markdown: "# První\n"}); err != nil {
		t.Fatal(err)
	}

	api.mu.Lock()
	api.hideUploader = true
	api.mu.Unlock()

	_, view, err := s.attachDocument(ctx, nil, attachDocumentIn{Card: cardID, Markdown: "# Druhý\n"})
	if err != nil {
		t.Fatal(err)
	}
	if view.Replaced != 0 {
		t.Errorf("replaced = %d — nothing may be deleted when the uploader cannot be established", view.Replaced)
	}
	if !strings.Contains(view.Note, "uploader the API did not name") {
		t.Errorf("the note must say why nothing was removed: %q", view.Note)
	}
	if rows := api.snapshot(); len(rows) != 2 {
		t.Errorf("card holds %d attachments, want both left in place: %+v", len(rows), rows)
	}
}

// Same account, same name, but not this tool's work — and it is replaced all the
// same. The HTML format could tell the two apart by the source block it sealed
// into every file it wrote; a plain .md carries no such marker, so the name plus
// the uploader is the whole test. Pinned rather than left implicit, because it
// is the one thing the change to Markdown gave up.
func TestAttachDocument_ReplacesAHandUploadedFileOfTheSameName(t *testing.T) {
	s, api := newDocumentServer(t)

	api.plant(t, noeto.Attachment{
		ID: "e0000000-0000-4000-8000-000000000003", UploadedByID: usID, UploadedBy: "Michal Bocek",
		Filename: defaultDocumentName, Status: noeto.AttachmentReady, CreatedAt: time.Now().Add(-time.Hour),
	}, []byte("# Ručně nahraný návrh\n"))

	_, view, err := s.attachDocument(context.Background(), nil, attachDocumentIn{Card: cardID, Markdown: "# Náš\n"})
	if err != nil {
		t.Fatal(err)
	}
	if view.Replaced != 1 {
		t.Errorf("replaced = %d, want the earlier copy of this name replaced", view.Replaced)
	}
	if view.Note != "" {
		t.Errorf("nothing was left behind, so there is nothing to note: %q", view.Note)
	}
	if rows := api.snapshot(); len(rows) != 1 {
		t.Errorf("card holds %d attachments, want only the new one: %+v", len(rows), rows)
	}
}

// A stale copy that will not delete is not a failed call: the new document is
// on the card, which is what was asked for. It is a reported one.
func TestAttachDocument_ReportsAFailedDeleteWithoutFailing(t *testing.T) {
	s, api := newDocumentServer(t)
	ctx := context.Background()

	if _, _, err := s.attachDocument(ctx, nil, attachDocumentIn{Card: cardID, Markdown: "# První\n"}); err != nil {
		t.Fatal(err)
	}

	api.mu.Lock()
	api.deleteStatus = http.StatusInternalServerError
	api.mu.Unlock()

	_, view, err := s.attachDocument(ctx, nil, attachDocumentIn{Card: cardID, Markdown: "# Druhý\n"})
	if err != nil {
		t.Fatalf("a failed cleanup must not fail the call: %v", err)
	}
	if view.Replaced != 0 {
		t.Errorf("replaced = %d, want 0 — nothing was actually removed", view.Replaced)
	}
	if !strings.Contains(view.Note, "could not be removed") {
		t.Errorf("note = %q, want it to name the failed cleanup", view.Note)
	}

	// And the document that matters is the readable one.
	api.mu.Lock()
	api.deleteStatus = 0
	api.mu.Unlock()

	_, back, err := s.readDocument(ctx, nil, readDocumentIn{Card: cardID})
	if err != nil {
		t.Fatal(err)
	}
	if back.Markdown != "# Druhý\n" {
		t.Errorf("read back %q, want the document that was just written", back.Markdown)
	}
}

// A 404 means the row was already gone, so nothing was replaced and the count
// must not claim otherwise.
func TestAttachDocument_AVanishedCopyIsNotCountedAsReplaced(t *testing.T) {
	s, api := newDocumentServer(t)
	ctx := context.Background()

	if _, _, err := s.attachDocument(ctx, nil, attachDocumentIn{Card: cardID, Markdown: "# První\n"}); err != nil {
		t.Fatal(err)
	}

	api.mu.Lock()
	api.deleteStatus = http.StatusNotFound
	api.mu.Unlock()

	_, view, err := s.attachDocument(ctx, nil, attachDocumentIn{Card: cardID, Markdown: "# Druhý\n"})
	if err != nil {
		t.Fatalf("an already-deleted copy is not an error: %v", err)
	}
	if view.Replaced != 0 {
		t.Errorf("replaced = %d, want 0 — the row was already gone", view.Replaced)
	}
	if view.Note != "" {
		t.Errorf("a vanished copy needs no note: %q", view.Note)
	}
}

// ── credentials ─────────────────────────────────────────────────────────────

// A presigned URL is a bearer credential: it grants unauthenticated read of the
// object for as long as the presign lives. Every net/http failure is a
// *url.Error whose message embeds the whole URL, query string included — so a
// DNS blip or a timeout is enough to put one in the model's context and in
// whatever the agent host writes to disk.
func TestDocument_NoPresignedURLReachesAnErrorMessage(t *testing.T) {
	const signature = "X-Amz-Signature=deadbeefcafe"

	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead.Close() // nothing is listening now, so every request fails in transport
	unreachable := dead.URL + "/objects/design.md?X-Amz-Algorithm=AWS4-HMAC-SHA256&" + signature

	t.Run("upload", func(t *testing.T) {
		s, api := newDocumentServer(t)
		api.mu.Lock()
		api.uploadURL = unreachable
		api.mu.Unlock()

		_, _, err := s.attachDocument(context.Background(), nil, attachDocumentIn{Card: cardID, Markdown: "# X\n"})
		if err == nil {
			t.Fatal("expected the unreachable store to surface as an error")
		}
		if strings.Contains(err.Error(), signature) || strings.Contains(err.Error(), "X-Amz") {
			t.Errorf("a presigned URL leaked into a model-visible error: %v", err)
		}
	})

	t.Run("download", func(t *testing.T) {
		s, api := newDocumentServer(t)
		api.mu.Lock()
		api.rows = append(api.rows, noeto.Attachment{
			ID: "e0000000-0000-4000-8000-000000000004", UploadedByID: usID, UploadedBy: "Michal Bocek",
			Filename: defaultDocumentName, Status: noeto.AttachmentReady, CreatedAt: time.Now(),
			DownloadURL: unreachable,
		})
		api.mu.Unlock()

		_, _, err := s.readDocument(context.Background(), nil, readDocumentIn{Card: cardID})
		if err == nil {
			t.Fatal("expected the unreachable store to surface as an error")
		}
		if strings.Contains(err.Error(), signature) || strings.Contains(err.Error(), "X-Amz") {
			t.Errorf("a presigned URL leaked into a model-visible error: %v", err)
		}
	})
}

// ── name matching ───────────────────────────────────────────────────────────

// Unicode simple folding — what strings.EqualFold does — treats U+017F as equal
// to "s", so a planted "deſign.md" would answer to "design.md" while
// looking like something else in the listing.
func TestSameName_DoesNotFoldUnicodeLookAlikes(t *testing.T) {
	if !sameName("Design.MD", "design.md") {
		t.Error("ASCII case must still fold — the API stores whatever case it was given")
	}
	if sameName("deſign.md", "design.md") {
		t.Error("a Unicode look-alike must not match")
	}
}

// Anybody on the team can upload a design.md, and the newest wins. Only one
// document can be returned, so the one thing that must not happen is the
// substitution being invisible.
func TestReadDocument_ReportsAShadowedDocument(t *testing.T) {
	s, api := newDocumentServer(t)
	ctx := context.Background()

	if _, _, err := s.attachDocument(ctx, nil, attachDocumentIn{Card: cardID, Markdown: "# Náš návrh\n"}); err != nil {
		t.Fatal(err)
	}

	api.plant(t, noeto.Attachment{
		ID: "e0000000-0000-4000-8000-000000000006", UploadedByID: themID, UploadedBy: "Jana Nováková",
		Filename: defaultDocumentName, Status: noeto.AttachmentReady, CreatedAt: time.Now().Add(time.Hour),
	}, []byte("# Janin návrh\n"))

	_, back, err := s.readDocument(ctx, nil, readDocumentIn{Card: cardID})
	if err != nil {
		t.Fatal(err)
	}
	if back.UploadedBy != "Jana Nováková" {
		t.Errorf("uploaded_by = %q, want the newest copy's uploader", back.UploadedBy)
	}
	if !strings.Contains(back.Note, "2 copies") {
		t.Errorf("note = %q, want it to say a choice was made", back.Note)
	}
}

// Exactly one leftover is the common case, and it must not read as
// "1 copy/copies".
func TestCopies_ReadsAsSomebodyWroteIt(t *testing.T) {
	if got := copies(1, "design.md"); got != "1 copy of design.md" {
		t.Errorf("copies(1) = %q", got)
	}
	if got := copies(3, "design.md"); got != "3 copies of design.md" {
		t.Errorf("copies(3) = %q", got)
	}
}

// The filename is what tells two documents on the same card apart, so the pair
// has to work under any name and a second name must not disturb the first.
// This is the whole reason the tools are named for the mechanism rather than
// for one kind of document.
func TestDocument_TwoNamesCoexistOnOneCard(t *testing.T) {
	s, api := newDocumentServer(t)
	ctx := context.Background()

	if _, _, err := s.attachDocument(ctx, nil,
		attachDocumentIn{Card: cardID, Markdown: "# Návrh\n"}); err != nil {
		t.Fatal(err)
	}
	_, second, err := s.attachDocument(ctx, nil,
		attachDocumentIn{Card: cardID, Markdown: "# Předání\n", Filename: "handover.md"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Filename != "handover.md" {
		t.Errorf("filename = %q, want the one that was asked for", second.Filename)
	}
	if second.Replaced != 0 {
		t.Errorf("replaced = %d — a different name must not supersede the first document", second.Replaced)
	}

	if rows := api.snapshot(); len(rows) != 2 {
		t.Fatalf("card holds %d attachments, want both documents: %+v", len(rows), rows)
	}

	for filename, want := range map[string]string{
		defaultDocumentName: "# Návrh\n",
		"handover.md":       "# Předání\n",
	} {
		_, back, err := s.readDocument(ctx, nil, readDocumentIn{Card: cardID, Filename: filename})
		if err != nil {
			t.Fatalf("read %s: %v", filename, err)
		}
		if back.Markdown != want {
			t.Errorf("%s read back %q, want %q", filename, back.Markdown, want)
		}
		if back.Note != "" {
			t.Errorf("%s: two differently named documents are not duplicates: %q", filename, back.Note)
		}
	}
}

// And replacing one leaves the other alone.
func TestDocument_ReplacingOneNameLeavesTheOther(t *testing.T) {
	s, api := newDocumentServer(t)
	ctx := context.Background()

	for _, in := range []attachDocumentIn{
		{Card: cardID, Markdown: "# Návrh v1\n"},
		{Card: cardID, Markdown: "# Předání\n", Filename: "handover.md"},
		{Card: cardID, Markdown: "# Návrh v2\n"},
	} {
		if _, _, err := s.attachDocument(ctx, nil, in); err != nil {
			t.Fatal(err)
		}
	}

	if rows := api.snapshot(); len(rows) != 2 {
		t.Fatalf("card holds %d attachments, want one of each name: %+v", len(rows), rows)
	}

	_, handover, err := s.readDocument(ctx, nil, readDocumentIn{Card: cardID, Filename: "handover.md"})
	if err != nil {
		t.Fatal(err)
	}
	if handover.Markdown != "# Předání\n" {
		t.Errorf("the untouched document changed: %q", handover.Markdown)
	}

	_, design, err := s.readDocument(ctx, nil, readDocumentIn{Card: cardID})
	if err != nil {
		t.Fatal(err)
	}
	if design.Markdown != "# Návrh v2\n" {
		t.Errorf("read back %q, want the replacement", design.Markdown)
	}
}
