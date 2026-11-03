package cli

import (
	"flag"
	"fmt"
	"io"
	"net/http"

	"github.com/ipoluianov/altesp/cli/mcp"
)

// cmdMCP serves the tools to an AI agent over stdin and stdout
func cmdMCP(args []string, e env) int {
	if len(args) > 0 {
		return usageError(e, fmt.Errorf("mcp takes no arguments"))
	}
	if err := mcp.Serve(e.stdin, e.stdout); err != nil {
		fmt.Fprintln(e.stderr, "mcp:", err)
		return exitFail
	}
	return exitOK
}

// defaultAPIAddr is where the local API listens by default
const defaultAPIAddr = "127.0.0.1:8765"

// cmdAPI serves the tools over HTTP on the loopback until interrupted
func cmdAPI(args []string, e env) int {
	fs := flag.NewFlagSet("api", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addr := fs.String("listen", defaultAPIAddr, "")
	if err := fs.Parse(args); err != nil {
		return usageError(e, err)
	}
	ln, err := mcp.ListenAPI(*addr)
	if err != nil {
		return fail(e, err)
	}
	fmt.Fprintf(e.stderr, "Local API on http://%s (GET /tools, POST /tools/<name>), Ctrl+C to stop\n", ln.Addr())
	ctx, stop := interrupted()
	defer stop()
	srv := &http.Server{Handler: mcp.APIHandler()}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return fail(e, err)
	}
	return exitOK
}
