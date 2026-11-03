package nvs

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"fmt"
)

// NVS encryption: ESP-IDF Programming Guide "NVS Encryption" and
// nvs_encrypted_partition.cpp, nvs_flash.h (ESP-IDF master, Nov 2026).
// Every 32-byte entry is one XTS-AES-256 data unit (IEEE 1619-2007); the
// data unit number (the tweak) is the entry's offset from the start of
// the partition as a little-endian number in 16 bytes. The XTS key is
// eky followed by tky of nvs_sec_cfg_t: eky encrypts the data, tky the
// tweak. Page headers and entry state bitmaps are not encrypted, and
// neither are empty entries (they stay 0xFF).

// Keys are the XTS keys of an encrypted NVS: nvs_sec_cfg_t of nvs_flash.h
type Keys struct {
	Data  [32]byte // eky
	Tweak [32]byte // tky
}

// The nvs_keys partition: eky (32 bytes), tky (32 bytes), then the CRC32
// of both, as nvs_flash_read_security_cfg checks it and nvs_partition_gen
// writes it (ESP-IDF Programming Guide "NVS Encryption", nvs_flash.c)
const keysLen = 64 + 4

// ParseKeys reads the keys from an nvs_keys partition. An erased
// partition (no keys yet) and a bad CRC (the partition is likely flash
// encrypted and was read as ciphertext) are errors.
func ParseKeys(b []byte) (Keys, error) {
	var k Keys
	if len(b) < keysLen {
		return k, fmt.Errorf("nvs: keys of %d bytes, need %d", len(b), keysLen)
	}
	if bytes.Count(b[:keysLen], []byte{0xFF}) == keysLen {
		return k, fmt.Errorf("nvs: the nvs_keys partition is empty")
	}
	if crc(0xFFFFFFFF, b[:64]) != binary.LittleEndian.Uint32(b[64:]) {
		return k, fmt.Errorf("nvs: bad CRC of nvs_keys (flash encrypted?)")
	}
	copy(k.Data[:], b[0:32])
	copy(k.Tweak[:], b[32:64])
	return k, nil
}

// Partition gives the content of an nvs_keys partition of the given size
// (4 KB usually) holding the keys
func (k Keys) Partition(size int) []byte {
	if size < keysLen {
		size = keysLen
	}
	b := bytes.Repeat([]byte{0xFF}, size)
	copy(b[0:], k.Data[:])
	copy(b[32:], k.Tweak[:])
	binary.LittleEndian.PutUint32(b[64:], crc(0xFFFFFFFF, b[:64]))
	return b
}

// xts is XTS-AES per IEEE 1619 for data units that are whole AES blocks
// (NVS entries are 32 bytes, so no ciphertext stealing is needed)
type xts struct {
	data, tweak cipher.Block
}

func newXTS(k Keys) (*xts, error) {
	d, err := aes.NewCipher(k.Data[:])
	if err != nil {
		return nil, err
	}
	t, err := aes.NewCipher(k.Tweak[:])
	if err != nil {
		return nil, err
	}
	return &xts{data: d, tweak: t}, nil
}

// crypt encrypts or decrypts b in place as the data unit with the
// given number
func (x *xts) crypt(b []byte, unit uint32, decrypt bool) {
	if len(b)%aes.BlockSize != 0 {
		panic("nvs: xts data unit is not whole blocks")
	}
	var t [aes.BlockSize]byte
	binary.LittleEndian.PutUint32(t[:], unit)
	x.tweak.Encrypt(t[:], t[:])
	for off := 0; off < len(b); off += aes.BlockSize {
		blk := b[off : off+aes.BlockSize]
		for i := range blk {
			blk[i] ^= t[i]
		}
		if decrypt {
			x.data.Decrypt(blk, blk)
		} else {
			x.data.Encrypt(blk, blk)
		}
		for i := range blk {
			blk[i] ^= t[i]
		}
		// T = T * alpha in GF(2^128), the bytes taken little-endian
		// (IEEE 1619 section 5.2)
		carry := t[15] >> 7
		for i := 15; i > 0; i-- {
			t[i] = t[i]<<1 | t[i-1]>>7
		}
		t[0] <<= 1
		if carry != 0 {
			t[0] ^= 0x87
		}
	}
}

func (x *xts) encrypt(b []byte, unit uint32) { x.crypt(b, unit, false) }
func (x *xts) decrypt(b []byte, unit uint32) { x.crypt(b, unit, true) }
