// Package mcp is the Model Context Protocol server of AltEsp over stdio,
// and the same tools as a local HTTP API: an AI agent or a script works
// with the ESP boards of the computer as the window does — tell what a
// board is, flash it, read and erase its flash, watch its log.
//
// The MCP transport is JSON-RPC 2.0, one JSON message per line. Adapted
// from the MCP server of AltSerialPort (same author, MIT).
package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"github.com/ipoluianov/altesp/app"
)

// The protocol versions known; a client asking for another gets the first
const latestVersion = "2025-06-18"

var knownVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// JSON-RPC error codes
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeNoMethod       = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return e.Message }

const instructions = "AltEsp gives you the ESP32-family boards connected to this computer. " +
	"Call list_ports, then board_info to learn the chip, its flash and partitions and what is flashed. " +
	"flash writes firmware files or a build folder (ESP-IDF, PlatformIO, Arduino) and verifies them; " +
	"read_flash saves flash to a file; erase_flash and erase_partition need confirm=\"ERASE\". " +
	"monitor captures the board's log for some seconds. Every call connects, works and resets the " +
	"board into its application, unless no_reset is set. The port can be omitted when exactly one board is connected."

// server is the state of one Serve
type server struct {
	w   io.Writer
	wmu sync.Mutex
}

// Serve answers the requests read from r, one JSON message per line, and
// writes the answers to w until r ends
func Serve(r io.Reader, w io.Writer) error {
	s := &server{w: w}
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			s.message(line)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func (s *server) send(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.wmu.Lock()
	s.w.Write(append(data, '\n'))
	s.wmu.Unlock()
}

// message handles a line: a request, a notification or a batch of them
func (s *server) message(line []byte) {
	line = bytes.TrimSpace(line)
	if line[0] == '[' {
		var batch []json.RawMessage
		if err := json.Unmarshal(line, &batch); err != nil || len(batch) == 0 {
			s.send(response{JSONRPC: "2.0", ID: json.RawMessage("null"),
				Error: &rpcError{Code: codeParse, Message: "invalid batch"}})
			return
		}
		var out []response
		for _, m := range batch {
			if r := s.one(m); r != nil {
				out = append(out, *r)
			}
		}
		if len(out) > 0 {
			s.send(out)
		}
		return
	}
	if r := s.one(line); r != nil {
		s.send(*r)
	}
}

// one handles a single message; nil for a notification
func (s *server) one(raw []byte) *response {
	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		return &response{JSONRPC: "2.0", ID: json.RawMessage("null"),
			Error: &rpcError{Code: codeParse, Message: "parse error: " + err.Error()}}
	}
	notification := len(req.ID) == 0 || string(req.ID) == "null"
	if req.Method == "" {
		if notification {
			return nil // a response from the client: nothing is asked of it
		}
		return &response{JSONRPC: "2.0", ID: req.ID,
			Error: &rpcError{Code: codeInvalidRequest, Message: "no method"}}
	}
	result, err := s.call(req.Method, req.Params)
	if notification {
		return nil
	}
	resp := &response{JSONRPC: "2.0", ID: req.ID, Result: result}
	if err != nil {
		var re *rpcError
		if !errors.As(err, &re) {
			re = &rpcError{Code: codeInternal, Message: err.Error()}
		}
		resp.Result = nil
		resp.Error = re
	} else if result == nil {
		resp.Result = struct{}{}
	}
	return resp
}

func (s *server) call(method string, params json.RawMessage) (any, error) {
	switch method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(params, &p)
		version := latestVersion
		for _, v := range knownVersions {
			if v == p.ProtocolVersion {
				version = v
			}
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "altesp", "version": app.Version},
			"instructions":    instructions,
		}, nil
	case "notifications/initialized", "notifications/cancelled":
		return nil, nil
	case "ping":
		return struct{}{}, nil
	case "tools/list":
		return map[string]any{"tools": toolList()}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: "invalid params: " + err.Error()}
		}
		t, ok := toolByName(p.Name)
		if !ok {
			return nil, &rpcError{Code: codeInvalidParams, Message: "unknown tool: " + p.Name}
		}
		if len(p.Arguments) == 0 || string(p.Arguments) == "null" {
			p.Arguments = json.RawMessage("{}")
		}
		return runTool(t, p.Arguments), nil
	}
	return nil, &rpcError{Code: codeNoMethod, Message: "method not found: " + method}
}

// toolResult is the result of tools/call
type toolResult struct {
	Content []content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

type content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// runTool calls the tool; its errors become a result with isError, which
// the agent sees and can act on
func runTool(t *tool, args json.RawMessage) toolResult {
	out, err := t.run(args)
	if err != nil {
		return toolResult{Content: []content{{Type: "text", Text: err.Error()}}, IsError: true}
	}
	text, ok := out.(string)
	if !ok {
		data, _ := json.MarshalIndent(out, "", "  ")
		text = string(data)
	}
	return toolResult{Content: []content{{Type: "text", Text: text}}}
}
