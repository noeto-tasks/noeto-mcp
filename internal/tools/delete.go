package tools

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rotisserie/eris"

	"noeto-mcp/internal/noeto"
)

func (t *server) registerDeletes(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "delete_attachment",
		Description: "Delete a file from a card — only one this token's own account uploaded, " +
			"such as a wrong attach_file. Give the filename as get_card lists it. When the " +
			"card holds several of that name, the newest of your own goes and the answer " +
			"says what is left. A file somebody else uploaded is refused, even for a manager: " +
			"ask them, or remove it in the web app. Delete only what the user asked to remove.",
		Annotations: deletes(),
	}, t.deleteAttachment)
}

// deletes marks a tool that removes something without putting anything in its
// place. Not idempotent: a second call takes the next older copy of the name.
func deletes() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		DestructiveHint: ptr(true),
		IdempotentHint:  false,
		OpenWorldHint:   ptr(false),
	}
}

type deleteAttachmentIn struct {
	Card     string `json:"card" jsonschema:"the card id, from get_board or find_cards"`
	Filename string `json:"filename" jsonschema:"the name it appears under in get_card"`
}

func (t *server) deleteAttachment(ctx context.Context, _ *mcp.CallToolRequest, in deleteAttachmentIn) (*mcp.CallToolResult, *deletedView, error) {
	if err := requireID("card", in.Card); err != nil {
		return nil, nil, err
	}
	filename := strings.TrimSpace(in.Filename)
	if filename == "" {
		return nil, nil, &badInput{
			field: "filename",
			want:  "the name of a file on the card, as get_card lists it",
			got:   in.Filename,
		}
	}

	me, err := t.api.Me(ctx)
	if err != nil {
		return nil, nil, err
	}
	// The API would let a manager's token delete anybody's file. This tool
	// holds every token to the uploader rule, so a model can only undo its own
	// account's work.
	if me.UserID == "" {
		return nil, nil, eris.New("the API did not say which member this token belongs to, so nothing was deleted")
	}

	list, err := t.api.ListAttachments(ctx, in.Card)
	if err != nil {
		return nil, nil, err
	}
	target, remaining := newestOwnNamed(list, filename, me.UserID)
	if target == nil {
		if remaining == 0 {
			return nil, nil, eris.Errorf("this card has no %s — attachments on it: %s", filename, filenames(list))
		}
		return nil, nil, eris.Errorf(
			"every %s on this card was uploaded by somebody else (or by somebody the API did not name), and only your own can be deleted here — ask the uploader, or remove it in the web app",
			filename)
	}

	if err := t.api.DeleteAttachment(ctx, target.ID); err != nil {
		return nil, nil, err
	}

	view := &deletedView{Filename: target.Filename, Bytes: target.SizeBytes}
	if remaining > 0 {
		view.Note = "the card still holds " + copies(remaining, target.Filename)
	}
	return nil, view, nil
}

// newestOwnNamed picks the newest ready copy of the name that us uploaded, and
// counts the copies of the name that would remain after deleting it.
func newestOwnNamed(list []noeto.Attachment, filename, us string) (*noeto.Attachment, int) {
	var target *noeto.Attachment
	var total int
	for i := range list {
		a := &list[i]
		if a.Status != noeto.AttachmentReady || !sameName(a.Filename, filename) {
			continue
		}
		total++
		if a.UploadedByID != us {
			continue
		}
		if target == nil || a.CreatedAt.After(target.CreatedAt) {
			target = a
		}
	}
	if target == nil {
		return nil, total
	}
	return target, total - 1
}
