package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ipoluianov/altesp/internal/ops"
)

// call runs one MCP session of the requests and returns the answers by id
func call(t *testing.T, reqs ...string) map[int]map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := Serve(strings.NewReader(strings.Join(reqs, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	answers := map[int]map[string]any{}
	sc := bufio.NewScanner(&out)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var r map[string]any
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("answer %q: %v", sc.Text(), err)
		}
		answers[int(r["id"].(float64))] = r
	}
	return answers
}

// text is the text of a tools/call answer and whether it is an error
func text(t *testing.T, r map[string]any) (string, bool) {
	t.Helper()
	res, ok := r["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", r)
	}
	isErr, _ := res["isError"].(bool)
	return res["content"].([]any)[0].(map[string]any)["text"].(string), isErr
}

func toolCall(id int, name, args string) string {
	return `{"jsonrpc":"2.0","id":` + itoa(id) + `,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestMCPSession(t *testing.T) {
	defer ops.SimulateBoard()()
	dir := t.TempDir()
	app := filepath.Join(dir, "blob.bin")
	os.WriteFile(app, bytes.Repeat([]byte{7}, 5000), 0o644)
	nvs := filepath.Join(dir, "nvs.bin")
	a := call(t,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		toolCall(3, "board_info", `{}`),
		toolCall(4, "erase_flash", `{}`),
		toolCall(5, "flash", `{"files":[{"path":"`+app+`","address":"0x200000"}]}`),
		toolCall(6, "read_partition", `{"name":"nvs","path":"`+nvs+`"}`),
		toolCall(7, "monitor", `{"seconds":1,"reset":true}`),
	)
	if a[1]["result"].(map[string]any)["serverInfo"].(map[string]any)["name"] != "altesp" {
		t.Fatalf("initialize: %v", a[1])
	}
	if n := len(a[2]["result"].(map[string]any)["tools"].([]any)); n != len(tools) {
		t.Fatalf("%d tools listed", n)
	}
	if s, isErr := text(t, a[3]); isErr || !strings.Contains(s, `"chip": "ESP32-C3"`) {
		t.Fatalf("board_info: %s", s)
	}
	if s, isErr := text(t, a[4]); !isErr || !strings.Contains(s, "confirm") {
		t.Fatalf("erase without confirm: %s", s)
	}
	if s, isErr := text(t, a[5]); isErr || !strings.Contains(s, "blob.bin at 0x200000") {
		t.Fatalf("flash: %s", s)
	}
	if st, err := os.Stat(nvs); err != nil || st.Size() != 0x6000 {
		t.Fatalf("read_partition wrote %v, %v", st, err)
	}
	if s, isErr := text(t, a[7]); isErr || !strings.Contains(s, "E (130) app: sensor not found") {
		t.Fatalf("monitor: %q", s)
	}
}

func TestAPI(t *testing.T) {
	defer ops.SimulateBoard()()
	srv := httptest.NewServer(APIHandler())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/tools/list_ports", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Text  string
		Error bool
	}
	json.NewDecoder(resp.Body).Decode(&r)
	resp.Body.Close()
	if r.Error || !strings.Contains(r.Text, "sim0") {
		t.Fatalf("list_ports: %+v", r)
	}
	// A page of another site must not drive the board
	req, _ := http.NewRequest("POST", srv.URL+"/tools/erase_flash", strings.NewReader(`{"confirm":"ERASE"}`))
	req.Header.Set("Origin", "https://example.com")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: %d", resp.StatusCode)
	}
	if _, err := ListenAPI("0.0.0.0:0"); err != ErrNotLoopback {
		t.Fatalf("ListenAPI on all addresses: %v", err)
	}
}
