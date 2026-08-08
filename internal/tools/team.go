package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (t *server) registerTeam(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_members",
		Description: "List the people in the team. Every write tool accepts a member " +
			"by name or email, so this is mainly for answering \"who is on this team\" " +
			"and for disambiguating when two people share a first name.",
		Annotations: readOnly(),
	}, t.listMembers)
}

type listMembersIn struct{}

func (t *server) listMembers(ctx context.Context, _ *mcp.CallToolRequest, _ listMembersIn) (*mcp.CallToolResult, []memberView, error) {
	members, err := t.api.ListMembers(ctx)
	if err != nil {
		return nil, nil, err
	}
	return nil, memberViews(members), nil
}
