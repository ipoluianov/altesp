package ops

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// A transfer broken by the line — a bad contact, a cable pulled for a
// moment, a burst of noise — is done again: the connection is made again
// at a lower rate and the work goes on from the chunk that broke. The
// flash is read and written in chunks for it.

const (
	// readChunk and writeChunk are the parts done again after a break
	readChunk  = 0x20000
	writeChunk = 0x40000
	// maxReconnects is how many times one operation connects again
	maxReconnects = 3
	// portBack is how long a port that went away is waited for
	portBack = 5 * time.Second
)

// retry decides on an error of a chunk: nil when the connection was made
// again and the chunk is to be done again, else the error to return.
// tries counts the reconnections of the operation.
func (s *Session) retry(ctx context.Context, cause error, tries *int) error {
	if ctx.Err() != nil || *tries >= maxReconnects || !s.canReconnect() {
		return cause
	}
	*tries++
	baud := s.board.Baud
	if s.board.Stub && !builtinUSB(s.board.Port) {
		baud = lowerBaud(baud)
	}
	if err := s.reconnect(ctx, baud); err != nil {
		return fmt.Errorf("%w (connecting again: %v)", cause, err)
	}
	return nil
}

// canReconnect tells whether the session can be made again by itself: not
// over JTAG, and not when the board is put into download mode by hand
func (s *Session) canReconnect() bool {
	return s.l != nil && s.js == nil && s.board.Reset != ResetManual
}

// errNoReconnect is a session that can't be made again by itself
var errNoReconnect = errors.New("the connection can't be made again by itself")

// reconnect opens the port again — a board unplugged for a moment may be
// back under another name — resets the chip into download mode and starts
// the loader at the rate. The card is read again; its notes are kept, with
// one about the break.
func (s *Session) reconnect(ctx context.Context, baud int) error {
	if !s.canReconnect() {
		return errNoReconnect
	}
	info := s.board.Port
	s.port.Close()
	wait, cancel := context.WithTimeout(ctx, portBack)
	defer cancel()
	if p, err := WaitPort(wait, info, false); err == nil {
		info = p
	}
	ns, err := connectOnce(ctx, info, Options{
		Reset: s.board.Reset,
		Baud:  baud,
		// Without the stub only when it was left out on purpose
		NoStub: !s.board.Stub && s.board.StubErr == nil,
	})
	if err != nil {
		return err
	}
	notes := s.board.Notes
	*s = *ns
	s.board.Notes = append(notes, fmt.Sprintf("The connection broke and was made again at %d baud", s.board.Baud))
	return nil
}
