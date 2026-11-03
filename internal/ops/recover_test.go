package ops

import (
	"bytes"
	"context"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/ipoluianov/altesp/internal/loader"
	"github.com/ipoluianov/altesp/internal/serial"
)

// breakingBoard is a board on a CH340 whose port is opened anew as a real
// one: each open is a new simulated line to the same flash. brk is called
// with each line.
func breakingBoard(t *testing.T, brk func(n int, sim *loader.Sim)) (serial.PortInfo, *[]*loader.Sim) {
	b := c3Board()
	b.Circuit = loader.ResetBridge
	var sims []*loader.Sim
	old := openPort
	openPort = func(serial.PortInfo) (serial.Port, error) {
		sim := loader.NewSim(b)
		sims = append(sims, sim)
		if brk != nil {
			brk(len(sims), sim)
		}
		return sim, nil
	}
	t.Cleanup(func() { openPort = old })
	return serial.PortInfo{Name: "/dev/ttyUSB0", Short: "ttyUSB0", USB: true, VID: 0x1a86, PID: 0x7523}, &sims
}

// A write broken in the middle goes on after a new connection, at a lower
// rate, and is verified whole
func TestWriteRecovers(t *testing.T) {
	info, sims := breakingBoard(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s, err := Connect(ctx, info, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx, false)
	data := make([]byte, 1<<20)
	rand.Read(data)
	(*sims)[0].BreakIn(20)
	if err := s.Flash(ctx, []Item{{Name: "app.bin", Addr: 0x100000, Data: data}}, FlashOptions{Verify: true}, nil); err != nil {
		t.Fatal(err)
	}
	if len(*sims) != 2 {
		t.Fatalf("%d connections", len(*sims))
	}
	if got := (*sims)[1].FlashData()[0x100000 : 0x100000+len(data)]; !bytes.Equal(got, data) {
		t.Fatal("the flash differs")
	}
	if s.Board().Baud != 460800 || !strings.Contains(strings.Join(s.Board().Notes, "\n"), "made again") {
		t.Fatalf("baud %d, notes %v", s.Board().Baud, s.Board().Notes)
	}
}

// A read broken in the middle goes on from the chunk that broke
func TestReadRecovers(t *testing.T) {
	info, sims := breakingBoard(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s, err := Connect(ctx, info, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx, false)
	(*sims)[0].BreakIn(3)
	got, err := s.Read(ctx, 0, 1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := (*sims)[0].FlashData()[:1<<20]; !bytes.Equal(got, want) {
		t.Fatal("the data differs")
	}
}

// A line that breaks on every connection: the error tells where, after
// the reconnections allowed
func TestReadGivesUp(t *testing.T) {
	// The commands of flash data a connection uses: the lines after the
	// first break at their first read after it
	perConnect := 0
	info, sims := breakingBoard(t, func(n int, sim *loader.Sim) {
		if n > 1 {
			sim.BreakIn(perConnect)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	s, err := Connect(ctx, info, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx, false)
	perConnect = (*sims)[0].DataCommands()
	(*sims)[0].BreakIn(1)
	_, err = s.Read(ctx, 0, 1<<20, nil)
	if err == nil || !strings.Contains(err.Error(), "reading at 0x") {
		t.Fatalf("error %v", err)
	}
	if len(*sims) != 1+maxReconnects {
		t.Fatalf("%d connections", len(*sims))
	}
}
