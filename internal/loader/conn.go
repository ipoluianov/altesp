package loader

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ipoluianov/altesp/internal/serial"
)

// conn reads the port in a goroutine of its own, since Read of a port
// blocks, and gives the packets with timeouts
type conn struct {
	port serial.Port

	packets chan []byte
	done    chan struct{}

	mu      sync.Mutex
	dec     slipDecoder
	readErr error
}

// packetQueue is how many packets may wait: the stub sends READ_FLASH data
// ahead of the acknowledgements
const packetQueue = 256

func newConn(port serial.Port) *conn {
	c := &conn{port: port, packets: make(chan []byte, packetQueue), done: make(chan struct{})}
	go c.readLoop()
	return c
}

func (c *conn) readLoop() {
	defer close(c.done)
	buf := make([]byte, 4096)
	for {
		n, err := c.port.Read(buf)
		if n > 0 {
			c.mu.Lock()
			c.dec.feed(buf[:n], func(p []byte) {
				select {
				case c.packets <- p:
				default:
					// Nobody reads: the oldest packet goes
					<-c.packets
					c.packets <- p
				}
			})
			c.mu.Unlock()
		}
		if err != nil {
			c.mu.Lock()
			c.readErr = err
			c.mu.Unlock()
			return
		}
	}
}

// ErrTimeout is a loader that didn't answer in time
var ErrTimeout = errors.New("no answer from the chip")

// send frames and writes a packet
func (c *conn) send(p []byte) error {
	_, err := c.port.Write(slipEncode(p))
	return err
}

// recv waits for the next packet
func (c *conn) recv(ctx context.Context, timeout time.Duration) ([]byte, error) {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case p := <-c.packets:
		return p, nil
	case <-t.C:
		return nil, ErrTimeout
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		// The packets read before the port closed are still given
		select {
		case p := <-c.packets:
			return p, nil
		default:
		}
		c.mu.Lock()
		err := c.readErr
		c.mu.Unlock()
		return nil, fmt.Errorf("port: %w", err)
	}
}

// dropPackets forgets the packets received so far; the text stays
func (c *conn) dropPackets() {
	for {
		select {
		case <-c.packets:
		default:
			return
		}
	}
}

// flush forgets the packets and the text received so far
func (c *conn) flush() {
	c.dropPackets()
	c.mu.Lock()
	c.dec.takeText()
	c.mu.Unlock()
}

// text returns what the board printed outside the packets since the last
// call: its boot messages
func (c *conn) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.dec.takeText())
}
