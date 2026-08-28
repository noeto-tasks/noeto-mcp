package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"noeto-mcp/internal/noeto"
)

func (t *server) registerTeam(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_members",
		Description: "List the people in the team. Every write tool accepts a member " +
			"by name or email, so this is mainly for answering \"who is on this team\" " +
			"and for disambiguating when two people share a first name.",
		Annotations: readOnly(),
	}, t.listMembers)

	mcp.AddTool(s, &mcp.Tool{
		Name: "whoami",
		Description: "Who this server's access token belongs to — the member behind every " +
			"write these tools make. Use it to attribute work, to filter a board down to " +
			"that person's own cards, or to assign a card to them. Never infer this from a " +
			"member whose name merely looks similar.",
		Annotations: readOnly(),
	}, t.whoami)
}

type whoamiIn struct{}

func (t *server) whoami(ctx context.Context, _ *mcp.CallToolRequest, _ whoamiIn) (*mcp.CallToolResult, *memberView, error) {
	me, err := t.api.Me(ctx)
	if err != nil {
		return nil, nil, err
	}
	view := memberViews([]noeto.Member{*me})[0]
	return nil, &view, nil
}

type listMembersIn struct{}

func (t *server) listMembers(ctx context.Context, _ *mcp.CallToolRequest, _ listMembersIn) (*mcp.CallToolResult, []memberView, error) {
	members, err := t.api.ListMembers(ctx)
	if err != nil {
		return nil, nil, err
	}
	return nil, memberViews(members), nil
}
