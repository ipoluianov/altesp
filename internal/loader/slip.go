package loader

// The loaders frame every packet with SLIP (RFC 1055): 0xC0 starts and
// ends a packet, and inside it 0xC0 and 0xDB are sent as 0xDB 0xDC and
// 0xDB 0xDD.

const (
	slipEnd    = 0xC0
	slipEsc    = 0xDB
	slipEscEnd = 0xDC
	slipEscEsc = 0xDD
)

// slipEncode frames a packet
func slipEncode(p []byte) []byte {
	out := make([]byte, 0, len(p)+len(p)/16+2)
	out = append(out, slipEnd)
	for _, b := range p {
		switch b {
		case slipEnd:
			out = append(out, slipEsc, slipEscEnd)
		case slipEsc:
			out = append(out, slipEsc, slipEscEsc)
		default:
			out = append(out, b)
		}
	}
	return append(out, slipEnd)
}

// slipDecoder takes the bytes from the line and gives the packets. The
// bytes between packets are not lost: before the loader runs, the board
// prints its boot messages, and they tell why it didn't enter download
// mode.
type slipDecoder struct {
	in     bool // inside a packet
	esc    bool
	packet []byte
	// text is the bytes seen outside the packets
	text []byte
}

// maxText is how much of the text between packets is kept
const maxText = 4096

// feed decodes the bytes and calls emit for every complete packet
func (d *slipDecoder) feed(data []byte, emit func([]byte)) {
	for _, b := range data {
		if !d.in {
			if b == slipEnd {
				d.in = true
				d.packet = d.packet[:0]
				continue
			}
			d.text = append(d.text, b)
			if len(d.text) > maxText {
				d.text = d.text[len(d.text)-maxText:]
			}
			continue
		}
		if d.esc {
			d.esc = false
			switch b {
			case slipEscEnd:
				d.packet = append(d.packet, slipEnd)
			case slipEscEsc:
				d.packet = append(d.packet, slipEsc)
			default:
				// A broken escape: the packet is lost, this byte may
				// start the next one
				d.in = b == slipEnd
				d.packet = d.packet[:0]
			}
			continue
		}
		switch b {
		case slipEnd:
			// Two 0xC0 in a row are the end of one packet and the start
			// of the next, or an empty packet the loader doesn't send:
			// the empty one stays open as the start
			if len(d.packet) == 0 {
				continue
			}
			p := make([]byte, len(d.packet))
			copy(p, d.packet)
			d.in = false
			d.packet = d.packet[:0]
			emit(p)
		case slipEsc:
			d.esc = true
		default:
			d.packet = append(d.packet, b)
		}
	}
}

// takeText returns the text seen between packets and forgets it
func (d *slipDecoder) takeText() []byte {
	t := d.text
	d.text = nil
	return t
}
