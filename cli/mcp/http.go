package mcp

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
)

// The local API: the tools of the MCP server over HTTP, for scripts that
// don't speak MCP. GET /tools lists them, POST /tools/<name> with the
// arguments as a JSON object calls one and answers
// {"text": ..., "error": bool}. It listens on the loopback only.

// ErrNotLoopback is an address other than the computer's own
var ErrNotLoopback = errors.New("the API listens on the loopback only (127.0.0.1 or [::1])")

// ListenAPI checks the address and opens it
func ListenAPI(addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, ErrNotLoopback
	}
	return net.Listen("tcp", addr)
}

// APIHandler serves the local API
func APIHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tools", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, toolList())
	})
	mux.HandleFunc("POST /tools/{name}", func(w http.ResponseWriter, r *http.Request) {
		// A page in a browser must not drive the board: only requests
		// without an Origin, or from the loopback itself
		if o := r.Header.Get("Origin"); o != "" && !loopbackOrigin(o) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		t, ok := toolByName(r.PathValue("name"))
		if !ok {
			http.Error(w, "unknown tool", http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(strings.TrimSpace(string(body))) == 0 {
			body = []byte("{}")
		}
		res := runTool(t, body)
		status := http.StatusOK
		if res.IsError {
			status = http.StatusUnprocessableEntity
		}
		writeJSON(w, status, map[string]any{"text": res.Content[0].Text, "error": res.IsError})
	})
	return mux
}

func loopbackOrigin(origin string) bool {
	for _, p := range []string{"http://127.0.0.1", "http://localhost", "http://[::1]"} {
		if origin == p || strings.HasPrefix(origin, p+":") {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}
