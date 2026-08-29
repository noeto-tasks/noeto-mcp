package tools

import (
	"context"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rotisserie/eris"

	"noeto-mcp/internal/noeto"
)

// A written document on a card, in the card's own storage.
//
// The pair is deliberately generic — one Markdown document under a filename the
// caller chooses — rather than a tool per kind of document. What varies between
// a design record, a decision log and a handover note is the name and the
// contents, not the mechanism, and a tool named after one use is a tool that
// gets a near-duplicate sibling the first time somebody wants another.
//
// The use it was built for is the card's memory. A card records what was asked;
// the git history of one of several repositories records what was changed.
// Neither records why this shape and not another one, which is what a person
// coming back in a month actually needs and what is expensive to reconstruct.
//
// The file is the Markdown, byte for byte. It used to be an HTML rendering with
// the source sealed into a script block, so that a copy in somebody's Downloads
// folder would open typeset on a double click. That bought a print stylesheet
// at the price of an encoding two repositories had to agree on forever, a
// rendered half that could drift from the source it was rendered from, and a
// reader that had to unseal a file before it could show anything of it.
// Markdown is legible unrendered, every editor and every diff tool already
// opens it, and read_document now simply returns the file.
//
// The two are a pair and were built as one: attach_document writes onto the
// card, read_document gives it back on the next pass. Writing without reading
// would be a decoration nobody consumes.

const (
	// defaultDocumentName is what the /card workflow keeps on every card, and
	// so the useful default — but it is only a default. The tool takes any
	// filename, and the name is the only thing that distinguishes two documents
	// on the same card.
	//
	// Whatever the name, it is one stable name overwritten in place. Versioned
	// filenames were considered and rejected: after three rounds nobody can
	// tell which of design-v2 and design-final is the one that counts.
	defaultDocumentName = "design.md"

	// documentContentType is what the file is signed and stored as.
	//
	// It says what the bytes are; it does not get them rendered. noeto serves
	// every text type as a download — the object store's inline allow-list is
	// images plus PDF, and text/* is deliberately outside it (storage.go,
	// Viewable) — so a click on the filename in the web app opens the document
	// in the page, from the file's own Markdown, rather than in a tab.
	documentContentType = "text/markdown"

	// maxSourceBytes bounds the document, on both ends of the pair.
	//
	// Both ends, because the two limits have to agree: a document that could be
	// written but not read back would break the whole point of the pair on the
	// pass that needed it most. Refused rather than truncated for the same
	// reason — a model handed a silently shortened document would write the next
	// version from it and delete the rest for good.
	//
	// Half a megabyte is far past anything a design document should be; a long
	// brainstorm artifact is fifteen kilobytes.
	maxSourceBytes = 512 << 10
)

func (t *server) registerDocuments(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "attach_document",
		Description: "Attach a Markdown document to a card as a file, replacing the previous " +
			"one of the same name that this token uploaded. " +
			"Name it with filename — that is what tells two documents on the same card " +
			"apart, and what read_document asks for; it defaults to design.md. " +
			"The file holds the Markdown itself, so read_document returns exactly what " +
			"you wrote. The card it hangs on already carries the title, so do not repeat " +
			"it. For a design document, write the reasoning behind the work — what was " +
			"chosen, what was rejected and why, what was assumed, what is still open — " +
			"rather than a description of what the code does, which is read from the code.",
		Annotations: replaces(),
	}, t.attachDocument)

	mcp.AddTool(s, &mcp.Tool{
		Name: "read_document",
		Description: "Read back a document attached to a card by attach_document. " +
			"Give the filename it was written under; it defaults to design.md. " +
			"Call it before writing over one, so a second pass builds on " +
			"the first instead of starting over. Answers with the Markdown, never with a link.",
		Annotations: readOnly(),
	}, t.readDocument)
}

type attachDocumentIn struct {
	Card     string `json:"card" jsonschema:"the card id, from get_board or find_cards"`
	Markdown string `json:"markdown" jsonschema:"the document itself; Markdown including tables and fenced code"`
	Filename string `json:"filename,omitempty" jsonschema:"what to call it on the card; defaults to design.md, and must end in .md"`
}

// attachDocument uploads the document, then removes the version it supersedes.
//
// The order is the whole safety property. Attachments cannot be edited — there
// is no PATCH — and the card has no unique constraint on (card, filename), so a
// replace is an upload followed by a delete and a duplicate is briefly visible.
// Done this way the card holds two documents for a moment and never zero.
// Deleting first would mean a failed upload strips the design off the card.
func (t *server) attachDocument(ctx context.Context, _ *mcp.CallToolRequest, in attachDocumentIn) (*mcp.CallToolResult, *documentView, error) {
	if err := requireID("card", in.Card); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(in.Markdown) == "" {
		return nil, nil, &badInput{field: "markdown", want: "non-empty", got: in.Markdown}
	}
	if len(in.Markdown) > maxSourceBytes {
		return nil, nil, eris.Errorf(
			"the document is %d bytes of Markdown and the limit is %d — split it across two filenames, or cut it down",
			len(in.Markdown), maxSourceBytes)
	}
	filename, err := documentFilename(in.Filename)
	if err != nil {
		return nil, nil, err
	}

	doc := []byte(in.Markdown)

	// Listed before the upload, so the new document can never be among the
	// candidates for deletion — no id comparison to get wrong, and no race with
	// anything else that lands on the card meanwhile. It is also the first
	// request out, so a card id that does not exist or belongs to another team
	// fails here rather than half way through an upload.
	previous, err := t.api.ListAttachments(ctx, in.Card)
	if err != nil {
		return nil, nil, err
	}

	ready, err := t.api.UploadAttachment(ctx, in.Card,
		noeto.NewAttachment{Filename: filename, ContentType: documentContentType, SizeBytes: int64(len(doc))}, doc)
	if err != nil {
		return nil, nil, err
	}

	view := &documentView{Filename: ready.Filename, Bytes: len(doc)}
	view.Replaced, view.Note = t.removeSuperseded(ctx, previous, filename, ready.UploadedByID)
	return nil, view, nil
}

// removeSuperseded deletes the copies the new document replaces, and reports
// everything it deliberately did not touch.
//
// One condition before anything is deleted, because this is the only delete in
// the whole server and it runs on a model's say-so: the same account uploaded
// it. The token acts as a real member, so a file of this name from somebody
// else is their work and is left where it is.
//
// There used to be a second. The document was an HTML file carrying its own
// Markdown sealed inside it, and that seal doubled as proof attach_document had
// written the file — which told our design document apart from one the same
// person had uploaded by hand under the same name. A plain .md carries no such
// marker, and giving it one would put back the encoding this format was chosen
// to be rid of. So name plus uploader is the whole test now, and the case that
// loses by it is narrow and worth naming: a design.md somebody uploaded
// themselves through the web UI is replaced by the next attach_document on that
// card, like any earlier version of ours.
//
// Anything it cannot establish is left alone and named in the note. That is the
// safe direction: a duplicate on the card is visible and somebody can remove
// it, whereas a file deleted on a guess is gone.
func (t *server) removeSuperseded(ctx context.Context, previous []noeto.Attachment, filename, us string) (int, string) {
	var replaced, foreign, unknownOwner int
	var failures []string

	for _, old := range previous {
		if !sameName(old.Filename, filename) {
			continue
		}
		switch {
		case us == "" || old.UploadedByID == "":
			// The API stopped saying who uploaded what. Rather than let the
			// comparison below collapse to "" == "" and delete everything, stop
			// and say so — a visible failure beats a silent destructive one.
			unknownOwner++
			continue
		case old.UploadedByID != us:
			foreign++
			continue
		}

		if err := t.api.DeleteAttachment(ctx, old.ID); err != nil {
			if noeto.NotFound(err) {
				// Already gone, or never ours to begin with. Nothing was
				// replaced, so it is not counted as one.
				continue
			}
			// Keep going: the remaining copies are independent, and stopping
			// here would leave more of them than necessary.
			failures = append(failures, err.Error())
			continue
		}
		replaced++
	}

	var notes []string
	if foreign > 0 {
		notes = append(notes, copies(foreign, filename)+" uploaded by somebody else, left alone")
	}
	if unknownOwner > 0 {
		notes = append(notes, copies(unknownOwner, filename)+" whose uploader the API did not name, left alone")
	}
	if len(failures) > 0 {
		notes = append(notes, "an earlier copy could not be removed: "+strings.Join(failures, "; "))
	}
	if len(notes) == 0 {
		return replaced, ""
	}
	return replaced, "the new document is attached, but the card now holds more than one " + filename + " — " +
		strings.Join(notes, "; ")
}

type readDocumentIn struct {
	Card     string `json:"card" jsonschema:"the card id, from get_board or find_cards"`
	Filename string `json:"filename,omitempty" jsonschema:"the name it was attached under; defaults to design.md"`
}

func (t *server) readDocument(ctx context.Context, _ *mcp.CallToolRequest, in readDocumentIn) (*mcp.CallToolResult, *documentSourceView, error) {
	if err := requireID("card", in.Card); err != nil {
		return nil, nil, err
	}
	filename, err := documentFilename(in.Filename)
	if err != nil {
		return nil, nil, err
	}

	list, err := t.api.ListAttachments(ctx, in.Card)
	if err != nil {
		return nil, nil, err
	}

	found, candidates := latestNamed(list, filename)
	if found == nil {
		return nil, nil, eris.Errorf("this card has no %s — attachments on it: %s", filename, filenames(list))
	}

	// Checked before the download, against the length the API signed into the
	// presigned PUT rather than against a claim. Deliberately not truncated: a
	// shortened document reads as a whole one, and the next attach_document
	// would write it back and lose the remainder.
	if found.SizeBytes > maxSourceBytes {
		return nil, nil, eris.Errorf(
			"%s is %s, past the %s limit for reading one back — open it from the card in a browser instead",
			found.Filename, size(found.SizeBytes), size(maxSourceBytes))
	}

	raw, err := t.api.DownloadAttachment(ctx, *found, maxSourceBytes)
	if err != nil {
		return nil, nil, err
	}

	// Anybody on the team can put a file of this name on the card, so what came
	// back is bytes rather than Markdown until this says otherwise.
	source := string(raw)
	if err := readableText(source); err != nil {
		return nil, nil, eris.Wrapf(err, "%s was not read as a document", found.Filename)
	}

	view := &documentSourceView{
		Filename:   found.Filename,
		UploadedBy: found.UploadedBy,
		When:       found.CreatedAt.Format(time.RFC3339),
		Markdown:   source,
	}
	if candidates > 1 {
		// Anybody on the team can upload, so the newest copy of this name is
		// not necessarily the one the last attach_document wrote. Only one can
		// be returned; saying that a choice was made is what keeps the
		// substitution from being invisible.
		view.Note = "this card holds " + copies(candidates, found.Filename) + "; the newest was returned"
		if found.UploadedBy != "" {
			view.Note += ", uploaded by " + found.UploadedBy
		}
	}
	return nil, view, nil
}

// documentFilename validates the name and supplies the default.
//
// Narrow on purpose. The API accepts far more than this — it only strips
// directory components and control characters — but these documents have one
// job, and a name that survives a download onto somebody's desktop is part of
// it. Hence: .md, because that is what says the file is Markdown to every
// editor, viewer and diff tool that will open it; and no colon, because macOS
// shows a colon in a filename as a slash in the Finder and the file becomes
// hard to talk about.
func documentFilename(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return defaultDocumentName, nil
	}

	if strings.ContainsAny(name, `/\`) || name != filepath.Base(name) {
		return "", &badInput{field: "filename", want: "a bare filename with no directory part", got: raw}
	}
	if strings.Contains(name, ":") {
		return "", &badInput{field: "filename", want: "a name without a colon — macOS shows one as a slash in the Finder", got: raw}
	}
	if strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", &badInput{field: "filename", want: "a name without control characters", got: raw}
	}
	// Bidirectional format characters reorder how the name reads without
	// changing what it is — the classic trick for making an extension look like
	// something else in a file listing. This name's whole job is to be read in
	// a Downloads folder.
	if strings.ContainsFunc(name, isBidiControl) {
		return "", &badInput{field: "filename", want: "a name without bidirectional format characters", got: raw}
	}
	if len(name) > 255 {
		return "", &badInput{field: "filename", want: "at most 255 characters", got: raw}
	}
	if !strings.EqualFold(filepath.Ext(name), ".md") {
		return "", &badInput{field: "filename", want: "a name ending in .md", got: raw}
	}
	return name, nil
}

// sameName compares filenames the way the people looking at them do. The API
// stores whatever case it was given, so "Design.md" and "design.md" are two
// rows — and to anybody reading the card they are one document uploaded twice.
//
// ASCII case only, deliberately. Unicode simple folding — what strings.EqualFold
// does — treats U+017F LATIN SMALL LETTER LONG S as equal to "s", so a file
// called "deſign.md" would answer to "design.md" while looking like something
// else in the listing. Nothing legitimate needs that.
func sameName(a, b string) bool { return foldASCII(a) == foldASCII(b) }

// isBidiControl reports the Unicode format characters that change the visual
// order of a string: the legacy marks and embeddings, and the isolates.
func isBidiControl(r rune) bool {
	switch {
	case r == 0x061C: // ARABIC LETTER MARK
		return true
	case r == 0x200E || r == 0x200F: // LEFT-TO-RIGHT / RIGHT-TO-LEFT MARK
		return true
	case r >= 0x202A && r <= 0x202E: // embeddings and overrides
		return true
	case r >= 0x2066 && r <= 0x2069: // isolates
		return true
	}
	return false
}

// copies renders a count of same-named files, so a note about exactly one does
// not read as "1 copy/copies".
func copies(n int, filename string) string {
	if n == 1 {
		return "1 copy of " + filename
	}
	return strconv.Itoa(n) + " copies of " + filename
}

func foldASCII(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, s)
}

// latestNamed picks the newest ready attachment with this name, and says how
// many it was picked from.
//
// There can be more than one: the card has no unique constraint on the
// filename, a replace is briefly a duplicate, and anybody on the team can
// upload whatever they like. Newest is usually the one the last successful
// attach_document wrote — usually, which is why the count comes back too.
func latestNamed(list []noeto.Attachment, filename string) (*noeto.Attachment, int) {
	var found *noeto.Attachment
	var candidates int
	for i := range list {
		a := &list[i]
		if a.Status != noeto.AttachmentReady || !sameName(a.Filename, filename) {
			continue
		}
		candidates++
		if found == nil || a.CreatedAt.After(found.CreatedAt) {
			found = a
		}
	}
	return found, candidates
}

// filenames lists what is on the card, so a miss tells the model what it could
// have asked for rather than only that it was wrong.
func filenames(list []noeto.Attachment) string {
	if len(list) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(list))
	for _, a := range list {
		names = append(names, a.Filename)
	}
	sort.Strings(names)

	// Bounded: the names come from whoever uploaded the files, and an error
	// message is not the place to pour an arbitrary amount of that into the
	// conversation. Enough to recognise the document you meant.
	const most = 20
	if len(names) > most {
		return strings.Join(names[:most], ", ") + ", and " + strconv.Itoa(len(names)-most) + " more"
	}
	return strings.Join(names, ", ")
}
