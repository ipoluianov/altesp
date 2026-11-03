package ops

import (
	"encoding/binary"
	"testing"

	"github.com/ipoluianov/altesp/internal/chips"
	"github.com/ipoluianov/altesp/internal/formats"
)

// bootloaderImage is a small ESP32 bootloader: one segment in RAM, DIO,
// 40 MHz, 4 MB, the checksum, no hash
func bootloaderImage() []byte {
	img := make([]byte, 24+8+64)
	img[0], img[1], img[2], img[3] = 0xE9, 1, 2, 0x20
	binary.LittleEndian.PutUint32(img[4:], 0x40080400)
	binary.LittleEndian.PutUint16(img[17:], 0xFFFF)
	binary.LittleEndian.PutUint32(img[24:], 0x3FFF0000)
	binary.LittleEndian.PutUint32(img[28:], 64)
	sum := byte(0xEF)
	for _, b := range img[32:] {
		sum ^= b
	}
	for (len(img)+1)%16 != 0 {
		img = append(img, 0)
	}
	return append(img, sum)
}

func TestPatchFlashParams(t *testing.T) {
	b := Board{Chip: chips.ByKey("esp32")}
	boot := bootloaderImage()
	if formats.Detect(boot) != formats.Bootloader {
		t.Fatalf("the test bootloader is detected as %v", formats.Detect(boot))
	}
	items := []Item{{Name: "bootloader.bin", Addr: 0x1000, Data: boot}, {Name: "app.bin", Addr: 0x10000, Data: []byte{1}}}
	out, err := PatchFlashParams(b, items, FlashParams{Mode: "QIO", Freq: "80MHz", Size: "16MB"})
	if err != nil {
		t.Fatal(err)
	}
	h := out[0].Data
	if h[2] != 0 || h[3] != 0x4F {
		t.Fatalf("header bytes %02X %02X, want 00 4F", h[2], h[3])
	}
	if boot[2] != 2 {
		t.Fatal("the original was changed")
	}
	if img, err := formats.ParseImage(h); err != nil || !img.ChecksumOK {
		t.Fatalf("patched image: %v", err)
	}
	if _, err := PatchFlashParams(b, items, FlashParams{Freq: "48MHz"}); err == nil {
		t.Fatal("an ESP32 took 48MHz")
	}
	if _, err := PatchFlashParams(b, items[1:], FlashParams{Mode: "DIO"}); err == nil {
		t.Fatal("patched without a bootloader")
	}
	if got := FlashSizes("esp32s3"); len(got) != 8 || got[7] != "128MB" {
		t.Fatalf("S3 sizes %v", got)
	}
}
