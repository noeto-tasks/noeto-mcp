// Package tools registers the noeto tool surface on an MCP server.
//
// Nine tools, shaped by what someone asks an agent to do rather than by what
// the API exposes. The API has thirty-five endpoints; a bridge that published
// all of them would spend the model's context on schemas for things it will
// never call (push subscriptions, invitation revocation, OAuth callbacks) and
// would still leave the common jobs — "what am I working on", "move this to
// done" — as multi-call sequences the model has to assemble.
//
// So the surface is small and the tools are verbs. What is deliberately absent:
//
//   - Creating boards, columns, and labels. An agent that sets a board up from
//     scratch is a different job from one that works on an existing board, and
//     including it costs every conversation the schema for something almost
//     none of them will use.
//   - Deletion of anything. Destroying work on a model's judgement is a poor
//     trade against the convenience; a card in the wrong column is recoverable
//     and a deleted one is not.
//   - Attachments. Uploading takes three requests through presigned URLs, and
//     download links are short-lived credentials a model must not repeat.
package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"noeto-mcp/internal/noeto"
)

type server struct {
	api *noeto.Client
}

// Register adds every noeto tool to s.
func Register(s *mcp.Server, api *noeto.Client) {
	t := &server{api: api}
	t.registerBoards(s)
	t.registerCards(s)
	t.registerTeam(s)
}

// readOnly and writes describe what a tool does to the board, so a host can
// decide what needs asking about before it runs.
//
// They earn their place through their defaults rather than their presence: a
// tool with no annotations is destructive and open-world, because that is what
// the spec assumes when the fields are absent. Neither is true here. Nothing
// deletes, and a token reaches one team — so every tool below says so, and the
// read-only five can be run without a prompt.
func readOnly() *mcp.ToolAnnotations {
	// DestructiveHint and IdempotentHint are left off: the spec reads them only
	// when ReadOnlyHint is false.
	return &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(false)}
}

// writes marks a tool that changes a board. idempotent is whether sending the
// same arguments twice leaves the board where one call would: setting a due
// date does, posting a comment does not.
func writes(idempotent bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		DestructiveHint: ptr(false),
		IdempotentHint:  idempotent,
		OpenWorldHint:   ptr(false),
	}
}

func ptr[T any](v T) *T { return &v }

// board loads a board by name or id, together with the team roster needed to
// render assignees.
//
// Two requests, issued together rather than in sequence: neither depends on the
// other, and a board snapshot is the first thing almost every conversation
// asks for.
func (t *server) board(ctx context.Context, nameOrID string) (*noeto.BoardDetail, []noeto.Member, error) {
	boardID := nameOrID
	if !isID(nameOrID) {
		boards, err := t.api.ListBoards(ctx)
		if err != nil {
			return nil, nil, err
		}
		id, err := match("board", nameOrID, boardIndex(boards))
		if err != nil {
			return nil, nil, err
		}
		boardID = id
	}

	type result struct {
		members []noeto.Member
		err     error
	}
	members := make(chan result, 1)
	go func() {
		list, err := t.api.ListMembers(ctx)
		members <- result{list, err}
	}()

	detail, err := t.api.GetBoard(ctx, boardID)
	roster := <-members

	if err != nil {
		return nil, nil, err
	}
	if roster.err != nil {
		return nil, nil, roster.err
	}
	return detail, roster.members, nil
}
