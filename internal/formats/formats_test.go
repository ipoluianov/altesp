package formats

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seg is a segment of a crafted image
type seg struct {
	addr uint32
	data []byte
}

// mkImage builds an image as the format describes it: header, extended
// header, segments, zeros, checksum, SHA-256
func mkImage(chipID uint16, entry uint32, segs []seg, hash bool) []byte {
	b := []byte{imageMagic, byte(len(segs)), 2, 0x20}
	b = binary.LittleEndian.AppendUint32(b, entry)
	x := make([]byte, extHeaderLen)
	x[0] = 0xEE
	binary.LittleEndian.PutUint16(x[4:], chipID)
	binary.LittleEndian.PutUint16(x[7:], 0)
	binary.LittleEndian.PutUint16(x[9:], 99)
	if hash {
		x[15] = 1
	}
	b = append(b, x...)
	sum := byte(checksumSeed)
	for _, s := range segs {
		b = binary.LittleEndian.AppendUint32(b, s.addr)
		b = binary.LittleEndian.AppendUint32(b, uint32(len(s.data)))
		b = append(b, s.data...)
		for _, c := range s.data {
			sum ^= c
		}
	}
	for len(b)%16 != 15 {
		b = append(b, 0)
	}
	b = append(b, sum)
	if hash {
		h := sha256.Sum256(b)
		b = append(b, h[:]...)
	}
	return b
}

func mkAppDesc(project, version string) []byte {
	d := make([]byte, appDescLen)
	binary.LittleEndian.PutUint32(d, appDescMagic)
	binary.LittleEndian.PutUint32(d[4:], 3)
	copy(d[16:], version)
	copy(d[48:], project)
	copy(d[80:], "12:34:56")
	copy(d[96:], "Nov  2 2026")
	copy(d[112:], "v5.5.1")
	for i := range 32 {
		d[144+i] = byte(i)
	}
	return d
}

// An ESP32-S3 application: DROM with the app description, IROM code, DRAM
func s3App() []byte {
	return mkImage(0x0009, 0x40375000, []seg{
		{0x3C020020, append(mkAppDesc("blink", "1.2.3"), bytes.Repeat([]byte{0x11}, 100)...)},
		{0x42000020, bytes.Repeat([]byte{0x22, 0x33}, 200)},
		{0x3FC90000, []byte{1, 2, 3, 4, 5}},
	}, true)
}

// An ESP32-S3 bootloader: RAM segments only, the bootloader description
func s3Boot() []byte {
	bd := make([]byte, 80)
	bd[0] = bootDescMagic
	copy(bd[8:], "v5.5.1")
	return mkImage(0x0009, 0x403C8000, []seg{
		{0x3FCD5000, bd},
		{0x403CC000, bytes.Repeat([]byte{0x44}, 3000)},
	}, true)
}

func TestParseApp(t *testing.T) {
	img := s3App()
	im, err := ParseImage(img)
	if err != nil {
		t.Fatal(err)
	}
	if im.ChipID != 9 || im.MaxRevFull != 99 || im.Entry != 0x40375000 || im.FlashMode != 2 || im.FlashFreqSize != 0x20 {
		t.Fatalf("header %+v", im)
	}
	if len(im.Segments) != 3 || im.Segments[0].Offset != 32 || im.Segments[1].Addr != 0x42000020 {
		t.Fatalf("segments %+v", im.Segments)
	}
	if !im.HashAppended || !im.ChecksumOK || !im.HashOK || im.Size != len(img) || im.Signed || im.IsBootloader {
		t.Fatalf("checks %+v", im)
	}
	a := im.App
	if a == nil || a.ProjectName != "blink" || a.Version != "1.2.3" || a.IDFVersion != "v5.5.1" ||
		a.Date != "Nov  2 2026" || a.Time != "12:34:56" || a.SecureVersion != 3 || a.ELFSHA256[31] != 31 {
		t.Fatalf("app %+v", a)
	}
	if k := Detect(img); k != App {
		t.Fatalf("detected %v", k)
	}
	// A byte of a segment changed: the checksum and the hash catch it
	bad := append([]byte(nil), img...)
	bad[im.Segments[1].Offset+5] ^= 1
	if im, _ := ParseImage(bad); im.ChecksumOK || im.HashOK {
		t.Fatalf("damage not seen: %+v", im)
	}
	// Cut images are errors
	if _, err := ParseImage(img[:len(img)-10]); err == nil {
		t.Fatal("cut hash accepted")
	}
	if _, err := ParseImage(img[:200]); err == nil {
		t.Fatal("cut segment accepted")
	}
}

func TestParseAppHead(t *testing.T) {
	img := s3App()
	im, ok := ParseAppHead(img[:AppHeadSize])
	if !ok || im.App.ProjectName != "blink" || im.ChipID != 9 || im.FlashMode != 2 || len(im.Segments) != 0 {
		t.Fatalf("%+v %v", im, ok)
	}
	if AppHeadSize != 0x120 {
		t.Fatalf("AppHeadSize 0x%X", AppHeadSize)
	}
	if _, ok := ParseAppHead(bytes.Repeat([]byte{0xFF}, AppHeadSize)); ok {
		t.Fatal("erased flash taken for an app")
	}
	if _, ok := ParseAppHead(s3Boot()[:AppHeadSize]); ok {
		t.Fatal("bootloader taken for an app")
	}
}

func TestBootloader(t *testing.T) {
	img := s3Boot()
	im, err := ParseImage(img)
	if err != nil {
		t.Fatal(err)
	}
	if !im.IsBootloader || im.App != nil {
		t.Fatalf("%+v", im)
	}
	if v, ok := HasBootloaderDesc(img, im); !ok || v != "v5.5.1" {
		t.Fatalf("bootloader desc %q %v", v, ok)
	}
	if k := Detect(img); k != Bootloader {
		t.Fatalf("detected %v", k)
	}
	// Without the description: only RAM segments still tell it
	plain := mkImage(0, 0x40080000, []seg{{0x3FFF0000, make([]byte, 64)}, {0x40078000, make([]byte, 64)}}, false)
	if im, _ := ParseImage(plain); !im.IsBootloader || im.HashAppended || im.HashOK {
		t.Fatalf("%+v", im)
	}
}

func TestSigned(t *testing.T) {
	img := s3App()
	im, _ := ParseImage(img)
	signed := append([]byte(nil), img...)
	for len(signed)%sigBlockAlign != 0 {
		signed = append(signed, 0xFF)
	}
	blk := make([]byte, 1216)
	blk[0], blk[1] = sigBlockMagic, 2
	signed = append(signed, blk...)
	if im2, _ := ParseImage(signed); !im2.Signed || im.Signed {
		t.Fatal("signature block not seen")
	}
}

func TestPatchHeader(t *testing.T) {
	boot := s3Boot()
	mode, fs := byte(3), byte(0x4F)
	out, err := PatchHeader(boot, &mode, &fs)
	if err != nil {
		t.Fatal(err)
	}
	im, _ := ParseImage(out)
	if im.FlashMode != 3 || im.FlashFreqSize != 0x4F || !im.HashOK || !im.ChecksumOK {
		t.Fatalf("%+v", im)
	}
	if boot[2] != 2 {
		t.Fatal("the original changed")
	}
	// nil keeps the byte
	out, _ = PatchHeader(boot, nil, &fs)
	if out[2] != 2 || out[3] != 0x4F {
		t.Fatalf("% X", out[:4])
	}
	// The bootloader of a merged ESP32 image, at 0x1000 after erased flash
	esp32Boot := mkImage(0, 0x40080000, []seg{{0x3FFF0000, make([]byte, 64)}, {0x40078000, make([]byte, 64)}}, true)
	merged := mkFlash(0x1000, esp32Boot, 0x8000, s3App())
	out, err = PatchHeader(merged, &mode, nil)
	if err != nil {
		t.Fatal(err)
	}
	if im, _ := ParseImage(out[0x1000:]); im.FlashMode != 3 || !im.HashOK {
		t.Fatalf("%+v", im)
	}
	// Signed: refused
	signed := append([]byte(nil), boot...)
	for len(signed)%sigBlockAlign != 0 {
		signed = append(signed, 0xFF)
	}
	signed = append(signed, sigBlockMagic, 3, 0, 0)
	if _, err := PatchHeader(signed, &mode, nil); err == nil || !strings.Contains(err.Error(), "signed") {
		t.Fatalf("signed image patched: %v", err)
	}
	// Damaged: refused
	bad := append([]byte(nil), boot...)
	bad[40] ^= 1
	if _, err := PatchHeader(bad, &mode, nil); err == nil {
		t.Fatal("damaged image patched")
	}
}

// part is an entry of a crafted partition table
func part(name string, typ, sub byte, off, size uint32) []byte {
	e := make([]byte, partEntryLen)
	binary.LittleEndian.PutUint16(e, partMagic)
	e[2], e[3] = typ, sub
	binary.LittleEndian.PutUint32(e[4:], off)
	binary.LittleEndian.PutUint32(e[8:], size)
	copy(e[12:28], name)
	return e
}

// mkTable builds a table as gen_esp32part does: entries, the MD5 entry,
// erased to 0xC00
func mkTable(md5Entry bool, entries ...[]byte) []byte {
	var b []byte
	for _, e := range entries {
		b = append(b, e...)
	}
	if md5Entry {
		sum := md5.Sum(b)
		e := bytes.Repeat([]byte{0xFF}, partEntryLen)
		binary.LittleEndian.PutUint16(e, partMagicMD5)
		copy(e[partMD5Off:], sum[:])
		b = append(b, e...)
	}
	for len(b) < partTableMax {
		b = append(b, 0xFF)
	}
	return b
}

func otaTable() []byte {
	return mkTable(true,
		part("nvs", PartData, SubDataNVS, 0x9000, 0x5000),
		part("otadata", PartData, SubDataOTA, 0xE000, 0x2000),
		part("app0", PartApp, 0x10, 0x10000, 0x140000),
		part("app1", PartApp, 0x11, 0x150000, 0x140000),
		part("spiffs", PartData, SubDataSPIFFS, 0x290000, 0x160000),
		part("coredump", PartData, 0x03, 0x3F0000, 0x10000),
	)
}

func TestPartitionTable(t *testing.T) {
	parts, err := ParsePartitionTable(otaTable())
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 6 || parts[2].Name != "app0" || parts[2].Offset != 0x10000 || parts[4].Size != 0x160000 {
		t.Fatalf("%+v", parts)
	}
	names := []string{}
	for _, p := range parts {
		names = append(names, p.TypeName()+"/"+p.SubTypeName())
	}
	if got := strings.Join(names, " "); got != "data/nvs data/ota app/ota_0 app/ota_1 data/spiffs data/coredump" {
		t.Fatal(got)
	}
	if !parts[3].IsApp() || parts[0].IsApp() {
		t.Fatal("IsApp")
	}
	if p, ok := AppPartition(parts); !ok || p.Name != "app0" {
		t.Fatalf("app partition %+v", p)
	}
	// A factory partition wins over the OTA slots
	withFactory := mkTable(true, part("ota_1", PartApp, 0x11, 0x110000, 0x100000),
		part("factory", PartApp, SubFactory, 0x10000, 0x100000))
	parts, _ = ParsePartitionTable(withFactory)
	if p, _ := AppPartition(parts); p.Name != "factory" {
		t.Fatalf("app partition %+v", p)
	}
	// No MD5 entry (older tables): fine; a wrong MD5: an error
	if _, err := ParsePartitionTable(mkTable(false, part("factory", PartApp, 0, 0x10000, 0x100000))); err != nil {
		t.Fatal(err)
	}
	bad := otaTable()
	bad[5] ^= 1
	if _, err := ParsePartitionTable(bad); err == nil {
		t.Fatal("wrong MD5 accepted")
	}
	if _, err := ParsePartitionTable(bytes.Repeat([]byte{0xFF}, 0xC00)); err == nil {
		t.Fatal("erased table accepted")
	}
	if k := Detect(otaTable()); k != PartitionTable {
		t.Fatalf("detected %v", k)
	}
	custom := Partition{Type: 0x40, SubType: 0x07}
	if custom.TypeName() != "0x40" || custom.SubTypeName() != "0x07" {
		t.Fatal(custom.TypeName(), custom.SubTypeName())
	}
}

// mkFlash lays out parts at their addresses over erased flash
func mkFlash(kv ...any) []byte {
	var flash []byte
	for i := 0; i < len(kv); i += 2 {
		at, data := kv[i].(int), kv[i+1].([]byte)
		for len(flash) < at+len(data) {
			flash = append(flash, 0xFF)
		}
		copy(flash[at:], data)
	}
	return flash
}

func TestMerged(t *testing.T) {
	// ESP32-S3: bootloader at 0, the table moved to 0xA000
	flash := mkFlash(0, s3Boot(), 0xA000, otaTable(), 0x10000, s3App())
	if off, ok := FindPartitionTable(flash); !ok || off != 0xA000 {
		t.Fatalf("table at 0x%X %v", off, ok)
	}
	if k := Detect(flash); k != Merged {
		t.Fatalf("detected %v", k)
	}
	if a := MergedAddr(flash); a != 0 {
		t.Fatalf("merged at 0x%X", a)
	}
	// ESP32: the bootloader at 0x1000
	esp32Boot := mkImage(0, 0x40080000, []seg{{0x3FFF0000, make([]byte, 64)}, {0x40078000, make([]byte, 64)}}, true)
	flash = mkFlash(0x1000, esp32Boot, 0x8000, otaTable())
	if k := Detect(flash); k != Merged || MergedAddr(flash) != 0 {
		t.Fatalf("detected %v", k)
	}
	// The same merged from 0x1000 on (merge-bin --target-offset 0x1000)
	if a := MergedAddr(flash[0x1000:]); a != 0x1000 {
		t.Fatalf("merged at 0x%X", a)
	}
	if _, ok := FindPartitionTable(s3Boot()); ok {
		t.Fatal("table in a bootloader")
	}
}

func TestOTAData(t *testing.T) {
	// As Arduino's boot_app0.bin: ota_seq 1 with its CRC, the second
	// entry with ota_seq 0 and no CRC
	d := bytes.Repeat([]byte{0xFF}, otaDataLen)
	binary.LittleEndian.PutUint32(d, 1)
	binary.LittleEndian.PutUint32(d[28:], 0x4743989A)
	binary.LittleEndian.PutUint32(d[sectorSize:], 0)
	if k := Detect(d); k != OTAData {
		t.Fatalf("detected %v", k)
	}
	// The CRC of ota_seq 0 in the ESP-IDF scheme is 0xFFFFFFFF: the
	// second entry is valid as it is
	if !OTAEntryValid(d) || !OTAEntryValid(d[sectorSize:]) {
		t.Fatal("OTA entry CRC")
	}
	binary.LittleEndian.PutUint32(d[sectorSize:], 2)
	if OTAEntryValid(d[sectorSize:]) {
		t.Fatal("OTA entry with a wrong CRC")
	}
	if k := Detect(bytes.Repeat([]byte{0xFF}, otaDataLen)); k != OTAData {
		t.Fatalf("initial OTA data detected as %v", k)
	}
	if k := Detect(bytes.Repeat([]byte{0xFF}, 3*sectorSize)); k != Unknown {
		t.Fatalf("erased flash detected as %v", k)
	}
}

func TestDataPartitions(t *testing.T) {
	// NVS: an active page, a full one, an erased one
	nvs := bytes.Repeat([]byte{0xFF}, 3*nvsPageSize)
	for i, st := range []uint32{0xFFFFFFFC, 0xFFFFFFFE} {
		p := nvs[i*nvsPageSize:]
		binary.LittleEndian.PutUint32(p, st)
		binary.LittleEndian.PutUint32(p[4:], uint32(i))
		p[8] = nvsVersion2
		binary.LittleEndian.PutUint32(p[28:], espCRC32(p[4:28]))
	}
	if k := Detect(nvs); k != NVS {
		t.Fatalf("NVS detected as %v", k)
	}

	lfs := bytes.Repeat([]byte{0xFF}, 8*4096)
	copy(lfs, []byte{1, 0, 0, 0, 0xF0, 0x0F, 0xFF, 0xF7})
	copy(lfs[8:], "littlefs")
	if k := Detect(lfs); k != LittleFS {
		t.Fatalf("LittleFS detected as %v", k)
	}

	boot := make([]byte, 512)
	copy(boot, []byte{0xEB, 0x3C, 0x90})
	copy(boot[3:], "MSDOS5.0")
	binary.LittleEndian.PutUint16(boot[11:], 4096)
	boot[13], boot[16] = 1, 1
	binary.LittleEndian.PutUint16(boot[14:], 1)
	boot[21] = 0xF8
	boot[510], boot[511] = 0x55, 0xAA
	if k := Detect(mkFlash(0, boot, 4*4096-1, []byte{0xFF})); k != FAT {
		t.Fatalf("FAT detected as %v", k)
	}
	// Under wear levelling: shifted by the dummy sector
	if k := Detect(mkFlash(0x1000, boot, 4*4096-1, []byte{0xFF})); k != FAT {
		t.Fatalf("FAT under WL detected as %v", k)
	}

	// SPIFFS: a block whose lookup page names an index page and a data
	// page of object 5
	spiffs := bytes.Repeat([]byte{0xFF}, 2*spiffsBlock)
	binary.LittleEndian.PutUint16(spiffs[0:], 0x8005)
	binary.LittleEndian.PutUint16(spiffs[2:], 0x0005)
	binary.LittleEndian.PutUint16(spiffs[1*spiffsPage:], 0x8005)
	binary.LittleEndian.PutUint16(spiffs[2*spiffsPage:], 0x0005)
	if k := Detect(spiffs); k != SPIFFS {
		t.Fatalf("SPIFFS detected as %v", k)
	}
	if k := Detect([]byte("hello, world")); k != Unknown {
		t.Fatalf("text detected as %v", k)
	}
}

func writeFiles(t *testing.T, dir string, files map[string][]byte) {
	t.Helper()
	for name, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func files(b *Build) string {
	var s []string
	for _, f := range b.Files {
		s = append(s, fmt.Sprintf("%s@0x%X:%s", filepath.Base(f.Path), f.Addr, f.Kind))
	}
	return strings.Join(s, " ")
}

// The flasher_args.json of an ESP-IDF v5 build, as the template writes it
const flasherArgsJSON = `{
    "write_flash_args" : [ "--flash-mode", "dio",
                           "--flash-size", "4MB",
                           "--flash-freq", "80m" ],
    "flash_settings" : {
        "flash_mode": "dio",
        "flash_size": "4MB",
        "flash_freq": "80m"
    },
    "flash_files" : {
        "0x0" : "bootloader/bootloader.bin",
        "0x10000" : "blink.bin",
        "0x8000" : "partition_table/partition-table.bin",
        "0xe000" : "ota_data_initial.bin"
    },
    "bootloader" : { "offset" : "0x0", "file" : "bootloader/bootloader.bin", "encrypted" : "false" },
    "app" : { "offset" : "0x10000", "file" : "blink.bin", "encrypted" : "false" },
    "partition-table" : { "offset" : "0x8000", "file" : "partition_table/partition-table.bin", "encrypted" : "false" },
    "otadata" : { "offset" : "0xe000", "file" : "ota_data_initial.bin", "encrypted" : "false" },
    "extra_esptool_args" : {
        "after"  : "hard-reset",
        "before" : "default-reset",
        "stub"   : true,
        "chip"   : "esp32s3"
    }
}`

func TestLoadIDF(t *testing.T) {
	proj := t.TempDir()
	writeFiles(t, proj, map[string][]byte{
		"build/flasher_args.json":                   []byte(flasherArgsJSON),
		"build/project_description.json":            []byte(`{"app_elf": "blink.elf", "target": "esp32s3"}`),
		"build/bootloader/bootloader.bin":           s3Boot(),
		"build/blink.bin":                           s3App(),
		"build/blink.elf":                           []byte("\x7fELF"),
		"build/partition_table/partition-table.bin": otaTable(),
		"build/ota_data_initial.bin":                bytes.Repeat([]byte{0xFF}, otaDataLen),
	})
	b, err := LoadBuild(proj)
	if err != nil {
		t.Fatal(err)
	}
	if b.Tool != "ESP-IDF" || b.Chip != "esp32s3" || b.FlashMode != "dio" || b.FlashFreq != "80m" || b.FlashSize != "4MB" ||
		b.ELF != filepath.Join(proj, "build", "blink.elf") {
		t.Fatalf("%+v", b)
	}
	want := "bootloader.bin@0x0:Bootloader partition-table.bin@0x8000:Partition table ota_data_initial.bin@0xE000:OTA data blink.bin@0x10000:Application"
	if got := files(b); got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
	// The build folder itself works too
	if b, err := LoadBuild(filepath.Join(proj, "build")); err != nil || len(b.Files) != 4 {
		t.Fatal(err)
	}
	// A file missing: an error, not a partial flash
	os.Remove(filepath.Join(proj, "build", "blink.bin"))
	if _, err := LoadBuild(proj); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestLoadPlatformIO(t *testing.T) {
	proj := t.TempDir()
	esp32Boot := mkImage(0, 0x40080000, []seg{{0x3FFF0000, make([]byte, 64)}, {0x40078000, make([]byte, 64)}}, true)
	esp32App := mkImage(0, 0x40081000, []seg{{0x3F400020, mkAppDesc("arduino-lib-builder", "esp-idf: v4.4.7")}}, true)
	// The table puts the app at 0x20000 and the OTA data at 0xD000
	table := mkTable(true,
		part("nvs", PartData, SubDataNVS, 0x9000, 0x4000),
		part("otadata", PartData, SubDataOTA, 0xD000, 0x2000),
		part("app0", PartApp, 0x10, 0x20000, 0x140000),
	)
	writeFiles(t, proj, map[string][]byte{
		"platformio.ini":                     []byte("[env:esp32dev]\n"),
		".pio/build/old/firmware.bin":        s3App(),
		".pio/build/esp32dev/firmware.bin":   esp32App,
		".pio/build/esp32dev/firmware.elf":   []byte("\x7fELF"),
		".pio/build/esp32dev/bootloader.bin": esp32Boot,
		".pio/build/esp32dev/partitions.bin": table,
		".pio/build/esp32dev/boot_app0.bin":  bytes.Repeat([]byte{0xFF}, otaDataLen),
	})
	// "old" was built before
	old := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(proj, ".pio/build/old/firmware.bin"), old, old)
	b, err := LoadBuild(proj)
	if err != nil {
		t.Fatal(err)
	}
	if b.Tool != "PlatformIO" || b.Chip != "esp32" || !strings.HasSuffix(b.ELF, "firmware.elf") {
		t.Fatalf("%+v", b)
	}
	want := "bootloader.bin@0x1000:Bootloader partitions.bin@0x8000:Partition table boot_app0.bin@0xD000:OTA data firmware.bin@0x20000:Application"
	if got := files(b); got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
	// The environment folder directly
	if b, err := LoadBuild(filepath.Join(proj, ".pio", "build", "esp32dev")); err != nil || len(b.Files) != 4 {
		t.Fatal(err)
	}
}

func TestLoadArduino(t *testing.T) {
	// Arduino IDE 2 export with flash_args (arduino-esp32 3.x)
	dir := t.TempDir()
	out := "build/esp32.esp32.esp32s3"
	writeFiles(t, dir, map[string][]byte{
		"Blink.ino":                       []byte("void setup(){}\nvoid loop(){}\n"),
		out + "/Blink.ino.bin":            s3App(),
		out + "/Blink.ino.elf":            []byte("\x7fELF"),
		out + "/Blink.ino.bootloader.bin": s3Boot(),
		out + "/Blink.ino.partitions.bin": otaTable(),
		out + "/boot_app0.bin":            bytes.Repeat([]byte{0xFF}, otaDataLen),
		out + "/Blink.ino.merged.bin":     mkFlash(0, s3Boot(), 0x8000, otaTable(), 0x10000, s3App()),
		out + "/flash_args": []byte("--flash-mode dio --flash-freq 80m --flash-size 4MB\n" +
			"0x0 Blink.ino.bootloader.bin\n0x8000 Blink.ino.partitions.bin\n0xe000 boot_app0.bin\n0x10000 Blink.ino.bin\n"),
	})
	b, err := LoadBuild(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := "Blink.ino.bootloader.bin@0x0:Bootloader Blink.ino.partitions.bin@0x8000:Partition table boot_app0.bin@0xE000:OTA data Blink.ino.bin@0x10000:Application"
	if got := files(b); got != want || b.Tool != "Arduino" || b.Chip != "esp32s3" || b.FlashSize != "4MB" || !strings.HasSuffix(b.ELF, "Blink.ino.elf") {
		t.Fatalf("%+v\n got %s\nwant %s", b, files(b), want)
	}
	// Without flash_args: the separate files, addresses from the table
	os.Remove(filepath.Join(dir, out, "flash_args"))
	b, err = LoadBuild(filepath.Join(dir, out))
	if err != nil {
		t.Fatal(err)
	}
	if got := files(b); got != want || b.FlashMode != "" {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
	// Only the merged image
	for _, f := range []string{"Blink.ino.bin", "Blink.ino.bootloader.bin", "Blink.ino.partitions.bin", "boot_app0.bin"} {
		os.Remove(filepath.Join(dir, out, f))
	}
	b, err = LoadBuild(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := files(b); got != "Blink.ino.merged.bin@0x0:Merged image" || b.Chip != "esp32s3" {
		t.Fatalf("%s %+v", got, b)
	}
}

func TestNoBuild(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string][]byte{"readme.txt": []byte("hi")})
	if _, err := LoadBuild(dir); err != ErrNoBuild {
		t.Fatal(err)
	}
}

func TestKindString(t *testing.T) {
	if Merged.String() != "Merged image" || Kind(99).String() != "Unknown" {
		t.Fatal(Merged.String())
	}
}
