package efuse

import (
	"encoding/binary"
	"math/bits"
)

// Coding is the coding scheme of a block
type Coding int

const (
	// CodingNone: the bits are stored as they are (BLOCK0 of every chip,
	// BLOCK1-3 of an ESP32 with CODING_SCHEME 0 or 3)
	CodingNone Coding = iota
	// Coding34: the ESP32's 3/4 scheme, 24 bytes of data per block, each 6
	// bytes followed by 2 check bytes
	Coding34
	// CodingRepeat: the ESP32's repeat scheme, 16 bytes stored twice
	CodingRepeat
	// CodingRS: Reed-Solomon RS(44,32), 12 check bytes per block (BLOCK1
	// and up of the later chips)
	CodingRS
)

func (c Coding) String() string {
	switch c {
	case Coding34:
		return "3/4"
	case CodingRepeat:
		return "repeat"
	case CodingRS:
		return "RS(44,32)"
	}
	return "none"
}

// dataWords is how many words of a block hold data under the coding
// (esp_efuse_utility.c of the ESP32: 192 bits with 3/4, 128 with repeat)
func (c Coding) dataWords(words int) int {
	switch c {
	case Coding34:
		return min(words, 6)
	case CodingRepeat:
		return min(words, 4)
	}
	return words
}

// encode34 is the ESP32's 3/4 coding of 24 bytes into 8 words: each 6
// bytes, then their XOR and the sum of (i+1) times the bits set in byte i.
// esp_efuse_utility_apply_34_encoding of components/efuse/esp32/
// esp_efuse_utility.c (ESP-IDF, Apache-2.0), the same algorithm.
func encode34(data []uint32) [8]uint32 {
	var in [24]byte
	for i := 0; i < 6 && i < len(data); i++ {
		binary.LittleEndian.PutUint32(in[4*i:], data[i])
	}
	var out [32]byte
	for g := 0; g < 4; g++ {
		src := in[6*g : 6*g+6]
		var xor, mul byte
		for i, b := range src {
			xor ^= b
			mul += byte((i + 1) * bits.OnesCount8(b))
		}
		copy(out[8*g:], src)
		out[8*g+6] = xor
		out[8*g+7] = mul
	}
	var w [8]uint32
	for i := range w {
		w[i] = binary.LittleEndian.Uint32(out[4*i:])
	}
	return w
}

// decode34 is the inverse: the 24 data bytes as 6 words and, per group of
// 6 bytes, whether its check bytes match
func decode34(raw [8]uint32) (data [6]uint32, bad [4]bool) {
	var in [32]byte
	for i, w := range raw {
		binary.LittleEndian.PutUint32(in[4*i:], w)
	}
	var out [24]byte
	for g := 0; g < 4; g++ {
		src := in[8*g : 8*g+6]
		var xor, mul byte
		for i, b := range src {
			xor ^= b
			mul += byte((i + 1) * bits.OnesCount8(b))
		}
		bad[g] = xor != in[8*g+6] || mul != in[8*g+7]
		copy(out[6*g:], src)
	}
	for i := range data {
		data[i] = binary.LittleEndian.Uint32(out[4*i:])
	}
	return data, bad
}

// encodeRepeat: 4 words stored twice (apply_repeat_encoding of the ESP32's
// esp_efuse_utility.c)
func encodeRepeat(data []uint32) [8]uint32 {
	var w [8]uint32
	for i := 0; i < 4 && i < len(data); i++ {
		w[i], w[i+4] = data[i], data[i]
	}
	return w
}

// Reed-Solomon RS(44,32) over GF(2^8) with the primitive polynomial
// x^8+x^4+x^3+x^2+1 (0x11D), the ESP32-C3 TRM v1.4, 4.3.1.3: 32 data bytes
// followed by 12 parity bytes.
//
// unverified: the TRM gives the polynomial and shows the encoder as a
// shift register figure that isn't in its text; the rest is the usual
// systematic encoder assumed here: generator (x-a^0)(x-a^1)...(x-a^11)
// with a = 2, the data bytes in block order (byte 0 of word 0 first) as
// the highest coefficients, the parity bytes highest coefficient first,
// written to PGM_CHECK_VALUE0-2 little endian as ESP-IDF copies the 12
// bytes of ets_efuse_rs_calculate (a ROM function without source). After
// a burn the chip's RD_RS_ERR shows a wrong parity as a failed block, and
// Burn reports it; this is the first thing to check on a real board.
const rsParity = 12

var gfExp [512]byte
var gfLog [256]byte
var rsGen []byte

func init() {
	x := 1
	for i := 0; i < 255; i++ {
		gfExp[i] = byte(x)
		gfLog[x] = byte(i)
		x <<= 1
		if x&0x100 != 0 {
			x ^= 0x11D
		}
	}
	for i := 255; i < 512; i++ {
		gfExp[i] = gfExp[i-255]
	}
	// g(x) = prod (x - a^i), highest coefficient first
	rsGen = []byte{1}
	for i := 0; i < rsParity; i++ {
		next := make([]byte, len(rsGen)+1)
		for j, c := range rsGen {
			next[j] ^= c
			next[j+1] ^= gfMul(c, gfExp[i])
		}
		rsGen = next
	}
}

func gfMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[int(gfLog[a])+int(gfLog[b])]
}

// rsBytes is a block as the 32 bytes RS works on: the words little
// endian, the missing words zero (BLOCK1 has 6 words, the ESP32-C2's 3;
// the TRM: "the RS check codes will be calculated with these bits all
// treated as 0")
func rsBytes(words []uint32) [32]byte {
	var b [32]byte
	for i := 0; i < 8 && i < len(words); i++ {
		binary.LittleEndian.PutUint32(b[4*i:], words[i])
	}
	return b
}

// rsEncode is the parity of a block, as the 3 words of PGM_CHECK_VALUE0-2
func rsEncode(words []uint32) [3]uint32 {
	data := rsBytes(words)
	var reg [rsParity]byte
	for _, d := range data {
		fb := d ^ reg[0]
		copy(reg[:], reg[1:])
		reg[rsParity-1] = 0
		if fb != 0 {
			for j := 0; j < rsParity; j++ {
				reg[j] ^= gfMul(fb, rsGen[j+1])
			}
		}
	}
	var w [3]uint32
	for i := range w {
		w[i] = binary.LittleEndian.Uint32(reg[4*i:])
	}
	return w
}

// rsCheck: the syndromes of a block and its parity are all zero
func rsCheck(words []uint32, parity [3]uint32) bool {
	data := rsBytes(words)
	var cw [32 + rsParity]byte
	copy(cw[:], data[:])
	for i, w := range parity {
		binary.LittleEndian.PutUint32(cw[32+4*i:], w)
	}
	for i := 0; i < rsParity; i++ {
		var s byte
		for _, c := range cw {
			s = gfMul(s, gfExp[i]) ^ c
		}
		if s != 0 {
			return false
		}
	}
	return true
}
