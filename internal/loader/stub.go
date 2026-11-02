package loader

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"
)

// The stubs are the release files of esp-flasher-stub (stubs/SOURCE.md)
//
//go:embed stubs/*.json
var stubFiles embed.FS

// StubImage is a stub loader: two segments loaded into RAM and its entry
type StubImage struct {
	Text      []byte
	TextStart uint32
	Data      []byte
	DataStart uint32
	Entry     uint32
}

type stubJSON struct {
	Entry     uint32 `json:"entry"`
	Text      string `json:"text"`
	TextStart uint32 `json:"text_start"`
	Data      string `json:"data"`
	DataStart uint32 `json:"data_start"`
}

// StubFor returns the stub of a chip by the name of its file: "esp32c3",
// "esp32p4-rev1"
func StubFor(name string) (*StubImage, error) {
	b, err := stubFiles.ReadFile("stubs/" + name + ".json")
	if err != nil {
		return nil, fmt.Errorf("no stub loader for %s", name)
	}
	var j stubJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, fmt.Errorf("stub %s: %w", name, err)
	}
	s := &StubImage{TextStart: j.TextStart, DataStart: j.DataStart, Entry: j.Entry}
	if s.Text, err = base64.StdEncoding.DecodeString(j.Text); err != nil {
		return nil, fmt.Errorf("stub %s: %w", name, err)
	}
	if s.Data, err = base64.StdEncoding.DecodeString(j.Data); err != nil {
		return nil, fmt.Errorf("stub %s: %w", name, err)
	}
	return s, nil
}

// Stubs lists the names of the stubs embedded
func Stubs() []string {
	entries, _ := fs.ReadDir(stubFiles, "stubs")
	var names []string
	for _, e := range entries {
		if n, ok := strings.CutSuffix(e.Name(), ".json"); ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// memBlock is the size of the MEM_DATA blocks
const memBlock = 0x1800

// stubGreeting is the only packet the stub sends unasked, when it starts
var stubGreeting = []byte("OHAI")

// LoadStub uploads the stub into RAM and starts it
func (l *Loader) LoadStub(ctx context.Context, s *StubImage) error {
	if l.stub {
		return nil
	}
	for _, seg := range []struct {
		data []byte
		addr uint32
	}{{s.Text, s.TextStart}, {s.Data, s.DataStart}} {
		if len(seg.data) == 0 {
			continue
		}
		if err := l.memWrite(ctx, seg.addr, seg.data); err != nil {
			return err
		}
	}
	// MEM_END: execute flag 0 means "jump to the entry"
	if _, err := l.command(ctx, opMemEnd, words(0, s.Entry), 0, defaultTimeout); err != nil {
		return err
	}
	deadline := time.Now().Add(defaultTimeout)
	for {
		p, err := l.conn.recv(ctx, time.Until(deadline))
		if err != nil {
			return fmt.Errorf("stub loader did not start: %w", err)
		}
		if string(p) == string(stubGreeting) {
			l.stub = true
			return nil
		}
	}
}

// memWrite loads data into the chip's RAM
func (l *Loader) memWrite(ctx context.Context, addr uint32, data []byte) error {
	blocks := (len(data) + memBlock - 1) / memBlock
	if _, err := l.command(ctx, opMemBegin, words(uint32(len(data)), uint32(blocks), memBlock, addr), 0, defaultTimeout); err != nil {
		return err
	}
	for seq := 0; seq < blocks; seq++ {
		chunk := data[seq*memBlock : min((seq+1)*memBlock, len(data))]
		payload := append(words(uint32(len(chunk)), uint32(seq), 0, 0), chunk...)
		if _, err := l.command(ctx, opMemData, payload, checksum(chunk), defaultTimeout); err != nil {
			return err
		}
	}
	return nil
}
