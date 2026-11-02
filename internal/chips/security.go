package chips

import (
	"fmt"
	"math/bits"
)

// Security is the protection of a chip: from the flags of
// GET_SECURITY_INFO (DecodeSecurity) or, on the ESP32, which lacks the
// command, from its eFuse (SecurityFromEFuse)
type Security struct {
	SecureBoot bool
	// FlashEncryption: an odd number of bits set in FlashCryptCnt
	FlashEncryption bool
	// SecureDownload: the ROM loader allows only a few commands
	SecureDownload bool
	// JTAGDisabled: by either eFuse, JTAGSoftDisabled tells the soft one
	// (it can be re-enabled through the HMAC peripheral)
	JTAGDisabled     bool
	JTAGSoftDisabled bool
	USBDisabled      bool

	SecureBootAggressiveRevoke bool
	// KeyRevoked are the revoked secure boot key digests 0..2
	KeyRevoked [3]bool
	// The cache of the flash is disabled in download mode: the flash
	// can't be read through it
	DownloadDCacheDisabled, DownloadICacheDisabled bool

	FlashCryptCnt byte
	// Raw are the flags as the chip sent them
	Raw uint32
}

// The flags of GET_SECURITY_INFO.
//
// unverified: the bit positions. The esptool documentation "Serial
// Protocol" names the field ("32 bits flags") but not its bits; these are
// the ROM's layout as commonly known, to be checked on boards with the
// features burned.
const (
	secFlagSecureBootEn      = 1 << 0
	secFlagAggressiveRevoke  = 1 << 1
	secFlagSecureDownload    = 1 << 2
	secFlagKeyRevoke0        = 1 << 3
	secFlagKeyRevoke1        = 1 << 4
	secFlagKeyRevoke2        = 1 << 5
	secFlagSoftDisJTAG       = 1 << 6
	secFlagHardDisJTAG       = 1 << 7
	secFlagDisUSB            = 1 << 8
	secFlagDisDownloadDCache = 1 << 9
	secFlagDisDownloadICache = 1 << 10
)

// DecodeSecurity decodes the flags and flash_crypt_cnt of
// GET_SECURITY_INFO
func DecodeSecurity(flags uint32, flashCryptCnt byte) Security {
	has := func(bit uint32) bool { return flags&bit != 0 }
	return Security{
		SecureBoot:                 has(secFlagSecureBootEn),
		FlashEncryption:            flashEncrypted(uint32(flashCryptCnt)),
		SecureDownload:             has(secFlagSecureDownload),
		JTAGDisabled:               has(secFlagHardDisJTAG) || has(secFlagSoftDisJTAG),
		JTAGSoftDisabled:           has(secFlagSoftDisJTAG),
		USBDisabled:                has(secFlagDisUSB),
		SecureBootAggressiveRevoke: has(secFlagAggressiveRevoke),
		KeyRevoked: [3]bool{
			has(secFlagKeyRevoke0), has(secFlagKeyRevoke1), has(secFlagKeyRevoke2),
		},
		DownloadDCacheDisabled: has(secFlagDisDownloadDCache),
		DownloadICacheDisabled: has(secFlagDisDownloadICache),
		FlashCryptCnt:          flashCryptCnt,
		Raw:                    flags,
	}
}

// flashEncrypted: flash encryption is on with an odd number of bits set
// in the counter (FLASH_CRYPT_CNT / SPI_BOOT_CRYPT_CNT in
// esp_efuse_table.csv)
func flashEncrypted(cnt uint32) bool { return bits.OnesCount32(cnt)%2 == 1 }

// The security fields of the ESP32's BLK0
// (components/efuse/esp32/esp_efuse_table.csv, ESP-IDF master at
// 185194117695)
var (
	esp32FlashCryptCnt  = field{0, 20, 7}
	esp32AbsDone0       = field{0, 196, 1} // secure boot v1
	esp32AbsDone1       = field{0, 197, 1} // secure boot v2
	esp32JTAGDisable    = field{0, 198, 1}
	esp32DisableDLCache = field{0, 201, 1}
)

// SecurityFromEFuse reads the protection from the eFuse, for the ESP32,
// whose ROM doesn't know GET_SECURITY_INFO. The other chips answer
// GET_SECURITY_INFO; for them it is an error.
func (c *Chip) SecurityFromEFuse(r Regs) (Security, error) {
	if c.Key != "esp32" {
		return Security{}, fmt.Errorf("%s: the security state comes from GET_SECURITY_INFO", c.Name)
	}
	e := &efuseReader{r: r, blocks: c.regs.efuse, words: map[uint32]uint32{}}
	var v [5]uint64
	for i, f := range []field{esp32FlashCryptCnt, esp32AbsDone0, esp32AbsDone1, esp32JTAGDisable, esp32DisableDLCache} {
		x, err := e.field(f)
		if err != nil {
			return Security{}, err
		}
		v[i] = x
	}
	return Security{
		SecureBoot:             v[1] != 0 || v[2] != 0,
		FlashEncryption:        flashEncrypted(uint32(v[0])),
		JTAGDisabled:           v[3] != 0,
		DownloadDCacheDisabled: v[4] != 0,
		DownloadICacheDisabled: v[4] != 0,
		FlashCryptCnt:          byte(v[0]),
	}, nil
}
