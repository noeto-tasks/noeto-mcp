package tools

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"noeto-mcp/internal/noeto"
)

// The spec types structuredContent as an object, so a tool whose output schema
// is an array is a protocol violation: a validating host rejects the result
// before it is ever read, and the tool is simply broken for that host. The Go
// SDK derives the schema from the handler's output type and will happily emit
// an array one, so this is the only place the invariant is enforced.
//
// v0.4.0 shipped list_boards, find_cards and list_members answering with a bare
// slice, and all three failed in Claude Code with "expected record, received
// array".
func TestEveryToolAnswersWithAnObject(t *testing.T) {
	ctx := context.Background()

	s := mcp.NewServer(&mcp.Implementation{Name: "noeto", Version: "test"}, nil)
	Register(s, noeto.New("http://127.0.0.1:0", "noeto_pat_test"))

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := s.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil).
		Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	tools, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) == 0 {
		t.Fatal("no tools registered")
	}

	for _, tool := range tools.Tools {
		schema, ok := tool.OutputSchema.(map[string]any)
		if !ok {
			t.Errorf("%s: output schema is %T, want a JSON schema object", tool.Name, tool.OutputSchema)
			continue
		}
		if schema["type"] != "object" {
			t.Errorf("%s: output schema type is %v, want object — structuredContent must not be an array",
				tool.Name, schema["type"])
		}
	}
}
