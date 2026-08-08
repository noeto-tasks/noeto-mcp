package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"noeto-mcp/internal/noeto"
)

// badInput is a caller mistake stated so the model can correct it in one turn:
// which field, what was expected, what arrived.
type badInput struct {
	field string
	want  string
	got   string
}

func (e *badInput) Error() string {
	return fmt.Sprintf("%s must be %s, got %q", e.field, e.want, e.got)
}

// clear is the sentinel a model sends to empty a nullable field. The API
// distinguishes "absent" from "null", and this is how that reaches it: an
// omitted argument leaves the field alone, the word "none" clears it.
const clear = "none"

func (t *server) registerCards(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "get_card",
		Description: "Read one card in full: its description and its comment thread. " +
			"get_board gives you titles; this gives you the detail behind one of them.",
		Annotations: readOnly(),
	}, t.getCard)

	mcp.AddTool(s, &mcp.Tool{
		Name: "create_card",
		Description: "Add a card to a column. The board and column may be named; " +
			"assignee and labels are resolved by name too.",
		// Not idempotent: calling it twice leaves two cards.
		Annotations: writes(false),
	}, t.createCard)

	mcp.AddTool(s, &mcp.Tool{
		Name: "update_card",
		Description: "Change a card's title, description, assignee, priority, due date " +
			"or labels. Omitted fields are left alone. Send \"none\" to clear the " +
			"assignee, priority or due date. Use move_card to change its column.",
		// Idempotent: the arguments are the field values, not a delta, so a
		// repeat sets them to what they already are.
		Annotations: writes(true),
	}, t.updateCard)

	mcp.AddTool(s, &mcp.Tool{
		Name: "move_card",
		Description: "Move a card to another column, or reorder it within one. " +
			"Position is relative: give before_card or after_card to place it next to " +
			"a specific card; with neither it goes to the bottom of the column.",
		// Idempotent: the destination is named, not stepped through, so a card
		// already there stays there.
		Annotations: writes(true),
	}, t.moveCard)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "comment_on_card",
		Description: "Post a comment on a card, as the account the access token belongs to.",
		// Not idempotent: calling it twice posts the comment twice.
		Annotations: writes(false),
	}, t.commentOnCard)
}

type cardIn struct {
	Card string `json:"card" jsonschema:"the card id, from get_board or find_cards"`
}

func (t *server) getCard(ctx context.Context, _ *mcp.CallToolRequest, in cardIn) (*mcp.CallToolResult, *cardDetailView, error) {
	if err := requireID("card", in.Card); err != nil {
		return nil, nil, err
	}

	detail, err := t.api.GetCard(ctx, in.Card)
	if err != nil {
		return nil, nil, err
	}

	// The card names its labels, so the board it belongs to has to be loaded
	// to translate them — the detail endpoint returns ids alone.
	board, members, err := t.board(ctx, detail.Card.BoardID)
	if err != nil {
		return nil, nil, err
	}
	render := newNames(board.Labels, members)

	view := cardDetailView{
		cardView:    render.card(detail.Card),
		Description: detail.Card.Description,
		Thread:      render.comments(detail.Comments),
	}
	view.Board = board.Board.Name
	view.Column = columnIndex(board.Columns)[detail.Card.ColumnID]
	return nil, &view, nil
}

type createCardIn struct {
	Board       string   `json:"board" jsonschema:"the board, by name or id"`
	Column      string   `json:"column" jsonschema:"the column to put it in, by name or id"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty" jsonschema:"Markdown is supported"`
	Assignee    string   `json:"assignee,omitempty" jsonschema:"a team member by name email or id"`
	Priority    string   `json:"priority,omitempty" jsonschema:"low, medium or high"`
	Due         string   `json:"due,omitempty" jsonschema:"YYYY-MM-DD"`
	Labels      []string `json:"labels,omitempty" jsonschema:"label names or ids; must already exist on the board"`
}

func (t *server) createCard(ctx context.Context, _ *mcp.CallToolRequest, in createCardIn) (*mcp.CallToolResult, *cardView, error) {
	board, members, err := t.board(ctx, in.Board)
	if err != nil {
		return nil, nil, err
	}

	columnID, err := match("column", in.Column, columnIndex(board.Columns))
	if err != nil {
		return nil, nil, err
	}

	body := noeto.NewCard{Title: strings.TrimSpace(in.Title), ColumnID: columnID}

	if in.Description != "" {
		body.Description = &in.Description
	}
	if in.Assignee != "" {
		id, err := matchMember(in.Assignee, members)
		if err != nil {
			return nil, nil, err
		}
		body.AssigneeID = &id
	}
	if in.Priority != "" {
		priority, err := normalizePriority(in.Priority)
		if err != nil {
			return nil, nil, err
		}
		body.Priority = &priority
	}
	if in.Due != "" {
		if _, err := parseDay(in.Due, "due"); err != nil {
			return nil, nil, err
		}
		body.DueDate = &in.Due
	}
	if len(in.Labels) > 0 {
		ids, err := resolveLabels(in.Labels, board.Labels)
		if err != nil {
			return nil, nil, err
		}
		body.LabelIDs = ids
	}

	card, err := t.api.CreateCard(ctx, board.Board.ID, body)
	if err != nil {
		return nil, nil, err
	}

	view := newNames(board.Labels, members).card(*card)
	view.Board = board.Board.Name
	view.Column = columnIndex(board.Columns)[card.ColumnID]
	return nil, &view, nil
}

type updateCardIn struct {
	Card        string   `json:"card" jsonschema:"the card id, from get_board or find_cards"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty" jsonschema:"replaces the whole description; Markdown is supported"`
	Assignee    string   `json:"assignee,omitempty" jsonschema:"a team member by name email or id, or \"none\" to unassign"`
	Priority    string   `json:"priority,omitempty" jsonschema:"low, medium, high, or \"none\" to clear"`
	Due         string   `json:"due,omitempty" jsonschema:"YYYY-MM-DD, or \"none\" to clear"`
	Labels      []string `json:"labels,omitempty" jsonschema:"replaces the card's labels entirely; an empty list removes them all"`
}

func (t *server) updateCard(ctx context.Context, _ *mcp.CallToolRequest, in updateCardIn) (*mcp.CallToolResult, *cardView, error) {
	if err := requireID("card", in.Card); err != nil {
		return nil, nil, err
	}

	current, err := t.api.GetCard(ctx, in.Card)
	if err != nil {
		return nil, nil, err
	}
	board, members, err := t.board(ctx, current.Card.BoardID)
	if err != nil {
		return nil, nil, err
	}

	var patch noeto.CardPatch

	if in.Title != "" {
		title := strings.TrimSpace(in.Title)
		patch.Title = &title
	}
	if in.Description != "" {
		patch.Description = &in.Description
	}

	// The three nullable fields share one shape: the sentinel means null on the
	// wire, anything else is a value, and absent stays absent.
	if in.Assignee != "" {
		if isClear(in.Assignee) {
			patch.AssigneeID = nil2()
		} else {
			id, err := matchMember(in.Assignee, members)
			if err != nil {
				return nil, nil, err
			}
			patch.AssigneeID = id
		}
	}
	if in.Priority != "" {
		if isClear(in.Priority) {
			patch.Priority = nil2()
		} else {
			priority, err := normalizePriority(in.Priority)
			if err != nil {
				return nil, nil, err
			}
			patch.Priority = priority
		}
	}
	if in.Due != "" {
		if isClear(in.Due) {
			patch.DueDate = nil2()
		} else {
			if _, err := parseDay(in.Due, "due"); err != nil {
				return nil, nil, err
			}
			patch.DueDate = in.Due
		}
	}
	if in.Labels != nil {
		ids, err := resolveLabels(in.Labels, board.Labels)
		if err != nil {
			return nil, nil, err
		}
		patch.LabelIDs = &ids
	}

	card, err := t.api.PatchCard(ctx, in.Card, patch)
	if err != nil {
		return nil, nil, err
	}

	view := newNames(board.Labels, members).card(*card)
	view.Board = board.Board.Name
	view.Column = columnIndex(board.Columns)[card.ColumnID]
	return nil, &view, nil
}

type moveCardIn struct {
	Card       string `json:"card" jsonschema:"the card id, from get_board or find_cards"`
	Column     string `json:"column,omitempty" jsonschema:"the destination column by name or id; omit to reorder within the current one"`
	BeforeCard string `json:"before_card,omitempty" jsonschema:"card id to place this one immediately above"`
	AfterCard  string `json:"after_card,omitempty" jsonschema:"card id to place this one immediately below"`
}

func (t *server) moveCard(ctx context.Context, _ *mcp.CallToolRequest, in moveCardIn) (*mcp.CallToolResult, *cardView, error) {
	if err := requireID("card", in.Card); err != nil {
		return nil, nil, err
	}
	if in.BeforeCard != "" && in.AfterCard != "" {
		return nil, nil, &badInput{
			field: "before_card and after_card",
			want:  "at most one of the two",
			got:   in.BeforeCard + " and " + in.AfterCard,
		}
	}
	for field, value := range map[string]string{"before_card": in.BeforeCard, "after_card": in.AfterCard} {
		if value != "" {
			if err := requireID(field, value); err != nil {
				return nil, nil, err
			}
		}
	}

	current, err := t.api.GetCard(ctx, in.Card)
	if err != nil {
		return nil, nil, err
	}
	board, members, err := t.board(ctx, current.Card.BoardID)
	if err != nil {
		return nil, nil, err
	}

	move := noeto.Move{BeforeCardID: in.BeforeCard, AfterCardID: in.AfterCard}
	if in.Column != "" {
		columnID, err := match("column", in.Column, columnIndex(board.Columns))
		if err != nil {
			return nil, nil, err
		}
		move.ColumnID = columnID
	}

	card, err := t.api.PatchCard(ctx, in.Card, noeto.CardPatch{Move: &move})
	if err != nil {
		return nil, nil, err
	}

	view := newNames(board.Labels, members).card(*card)
	view.Board = board.Board.Name
	view.Column = columnIndex(board.Columns)[card.ColumnID]
	return nil, &view, nil
}

type commentIn struct {
	Card string `json:"card" jsonschema:"the card id, from get_board or find_cards"`
	Body string `json:"body" jsonschema:"the comment text; Markdown is supported"`
}

func (t *server) commentOnCard(ctx context.Context, _ *mcp.CallToolRequest, in commentIn) (*mcp.CallToolResult, *commentView, error) {
	if err := requireID("card", in.Card); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(in.Body) == "" {
		return nil, nil, &badInput{field: "body", want: "non-empty", got: in.Body}
	}

	comment, err := t.api.AddComment(ctx, in.Card, in.Body)
	if err != nil {
		return nil, nil, err
	}
	view := names{}.comments([]noeto.Comment{*comment})[0]
	return nil, &view, nil
}

func isClear(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), clear)
}

// nil2 returns a typed nil that marshals to JSON null.
//
// The patch fields are `any` so they can carry three states, and a bare Go nil
// in an `any` field is indistinguishable from "unset" once `omitempty` sees it.
// A pointer-to-nothing survives that and serializes as null, which is what the
// API reads as "clear this".
func nil2() any {
	var null *string
	return null
}

func normalizePriority(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "low":
		return "low", nil
	case "medium", "med", "normal":
		return "medium", nil
	case "high", "urgent":
		return "high", nil
	default:
		return "", &badInput{field: "priority", want: "low, medium or high", got: value}
	}
}

func resolveLabels(wanted []string, available []noeto.Label) ([]string, error) {
	index := labelIndex(available)
	ids := make([]string, 0, len(wanted))
	for _, want := range wanted {
		id, err := match("label", want, index)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}
