// Command noeto-mcp serves the noeto kanban board to an AI agent over the
// Model Context Protocol.
//
// It runs on stdio: the agent's host starts this process and talks to it over
// stdin and stdout, which is why nothing here may ever write to stdout except
// the protocol. Diagnostics go to stderr.
//
// Configuration is two environment variables:
//
//	NOETO_TOKEN     a personal access token (noeto_pat_…), issued in noeto
//	                under Settings → Access tokens
//	NOETO_API_URL   the API root, default http://localhost:8081/api/v1
//
// The token is bound to the team it was issued in, so one process serves one
// team. That is deliberate on the API's side — it is what makes the blast
// radius of a leaked token knowable — and it means two teams need two entries
// in the agent host's config, each with its own token.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"noeto-mcp/internal/noeto"
	"noeto-mcp/internal/tools"
)

// version is stamped at link time by the Makefile.
var version = "dev"

const defaultAPIURL = "http://localhost:8081/api/v1"

func main() {
	log.SetFlags(0)
	log.SetPrefix("noeto-mcp: ")
	// Explicit, though it is already the default: on a stdio server, a stray
	// write to stdout corrupts the protocol stream, and the failure looks like
	// the agent host mis-parsing rather than like a log line.
	log.SetOutput(os.Stderr)

	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	token := strings.TrimSpace(os.Getenv("NOETO_TOKEN"))
	if token == "" {
		return fmt.Errorf("NOETO_TOKEN is not set — issue a token in noeto under Settings → Access tokens")
	}
	// Caught here rather than on the first 401, because a token pasted with the
	// wrong prefix is a copy-paste error and saying so at startup is cheaper
	// than an authentication failure three tool calls later.
	if !strings.HasPrefix(token, "noeto_pat_") {
		return fmt.Errorf("NOETO_TOKEN does not look like a noeto access token (expected a noeto_pat_ prefix)")
	}

	apiURL := strings.TrimSpace(os.Getenv("NOETO_API_URL"))
	if apiURL == "" {
		apiURL = defaultAPIURL
	}

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "noeto",
		Title:   "Noeto boards",
		Version: version,
	}, nil)
	tools.Register(server, noeto.New(apiURL, token))

	// Stop cleanly when the host goes away, so an interrupted agent does not
	// leave a process holding a token.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("serving %s over stdio (version %s)", apiURL, version)
	return server.Run(ctx, &mcp.StdioTransport{})
}
