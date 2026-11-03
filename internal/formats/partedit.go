package formats

import (
	"bufio"
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// Building and checking partition tables, and their CSV form, as the
// Programming Guide "Partition Tables" describes them (ESP-IDF master, Nov
// 2026): "# Name, Type, SubType, Offset, Size, Flags", types app/data or a
// number, sizes in bytes, K or M, an empty offset placed after the
// previous partition, aligned (app 0x10000, the others 0x1000).

// Alignment of the partitions
const (
	AppAlign  = 0x10000
	DataAlign = 0x1000
)

// BuildPartitionTable writes the table: the entries, the MD5 entry and
// erased bytes up to 0xC00
func BuildPartitionTable(parts []Partition) ([]byte, error) {
	if len(parts) > partTableMax/partEntryLen-2 {
		return nil, fmt.Errorf("too many partitions: %d", len(parts))
	}
	t := make([]byte, 0, partTableMax)
	for _, p := range parts {
		if len(p.Name) > 16 {
			return nil, fmt.Errorf("partition name %q is longer than 16 bytes", p.Name)
		}
		e := make([]byte, partEntryLen)
		binary.LittleEndian.PutUint16(e, partMagic)
		e[2], e[3] = p.Type, p.SubType
		binary.LittleEndian.PutUint32(e[4:], p.Offset)
		binary.LittleEndian.PutUint32(e[8:], p.Size)
		copy(e[12:28], p.Name)
		binary.LittleEndian.PutUint32(e[28:], p.Flags)
		t = append(t, e...)
	}
	sum := md5.Sum(t)
	m := make([]byte, partEntryLen)
	binary.LittleEndian.PutUint16(m, partMagicMD5)
	for i := 2; i < partMD5Off; i++ {
		m[i] = 0xFF
	}
	copy(m[partMD5Off:], sum[:])
	t = append(t, m...)
	for len(t) < partTableMax {
		t = append(t, 0xFF)
	}
	return t, nil
}

// CheckPartitions finds what is wrong with a table: overlaps, alignment,
// partitions past the flash or over the table, repeated names, more than
// one otadata or factory. tableAddr is where the table goes.
func CheckPartitions(parts []Partition, tableAddr uint32, flashSize int) []string {
	var problems []string
	add := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	names := map[string]bool{}
	sorted := append([]Partition(nil), parts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Offset < sorted[j].Offset })
	tableEnd := tableAddr + DataAlign
	counts := map[string]int{}
	for i, p := range sorted {
		if p.Name == "" {
			add("a partition at 0x%X has no name", p.Offset)
		} else if names[p.Name] {
			add("the name %s is used twice", p.Name)
		}
		names[p.Name] = true
		if p.Size == 0 {
			add("%s has no size", p.Name)
		}
		align := uint32(DataAlign)
		if p.Type == PartApp {
			align = AppAlign
		}
		if p.Offset%align != 0 {
			add("%s at 0x%X is not aligned to 0x%X", p.Name, p.Offset, align)
		}
		if p.Offset < tableEnd && p.Offset+p.Size > tableAddr {
			add("%s overlaps the partition table at 0x%X", p.Name, tableAddr)
		}
		if flashSize > 0 && uint64(p.Offset)+uint64(p.Size) > uint64(flashSize) {
			add("%s ends past the end of the flash", p.Name)
		}
		if i > 0 {
			q := sorted[i-1]
			if q.Offset+q.Size > p.Offset {
				add("%s overlaps %s", p.Name, q.Name)
			}
		}
		switch {
		case p.Type == PartData && p.SubType == SubDataOTA:
			counts["otadata"]++
		case p.Type == PartApp && p.SubType == SubFactory:
			counts["factory"]++
		}
	}
	for what, n := range counts {
		if n > 1 {
			add("more than one %s partition", what)
		}
	}
	return problems
}

// csvTypes and csvSubtypes are the names of the CSV
var csvTypes = map[string]byte{"app": PartApp, "data": PartData, "bootloader": PartBootloader, "partition_table": PartPartitionTable}

var csvAppSubtypes = map[string]byte{"factory": SubFactory, "test": SubTest}

var csvDataSubtypes = map[string]byte{
	"ota": 0x00, "phy": 0x01, "nvs": 0x02, "coredump": 0x03, "nvs_keys": 0x04,
	"efuse": 0x05, "undefined": 0x06, "esphttpd": 0x80, "fat": 0x81, "spiffs": 0x82, "littlefs": 0x83,
}

// ReadPartitionsCSV reads a table in the CSV form. Partitions without an
// offset are placed after the previous one, starting after the table at
// tableAddr.
func ReadPartitionsCSV(r io.Reader, tableAddr uint32) ([]Partition, error) {
	var parts []Partition
	next := tableAddr + DataAlign
	sc := bufio.NewScanner(r)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		f := strings.Split(text, ",")
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}
		for len(f) < 6 {
			f = append(f, "")
		}
		bad := func(what string) error { return fmt.Errorf("line %d: %s", line, what) }
		p := Partition{Name: f[0]}
		typ, ok := csvTypes[strings.ToLower(f[1])]
		if !ok {
			n, err := parseCSVNumber(f[1])
			if err != nil || n > 0xFE {
				return nil, bad("unknown type " + f[1])
			}
			typ = byte(n)
		}
		p.Type = typ
		sub, err := parseSubtype(typ, f[2])
		if err != nil {
			return nil, bad(err.Error())
		}
		p.SubType = sub
		align := uint32(DataAlign)
		if p.Type == PartApp {
			align = AppAlign
		}
		if f[3] == "" {
			p.Offset = (next + align - 1) / align * align
		} else if p.Offset, err = parseCSVNumber(f[3]); err != nil {
			return nil, bad("offset " + f[3])
		}
		if p.Size, err = parseCSVNumber(f[4]); err != nil {
			return nil, bad("size " + f[4])
		}
		for _, flag := range strings.Split(f[5], ":") {
			switch strings.ToLower(strings.TrimSpace(flag)) {
			case "":
			case "encrypted":
				p.Flags |= FlagEncrypted
			case "readonly":
				p.Flags |= FlagReadOnly
			default:
				return nil, bad("unknown flag " + flag)
			}
		}
		next = p.Offset + p.Size
		parts = append(parts, p)
	}
	return parts, sc.Err()
}

func parseSubtype(typ byte, s string) (byte, error) {
	l := strings.ToLower(s)
	switch typ {
	case PartApp:
		if v, ok := csvAppSubtypes[l]; ok {
			return v, nil
		}
		if strings.HasPrefix(l, "ota_") {
			n, err := strconv.Atoi(l[4:])
			if err == nil && n >= 0 && n <= 15 {
				return byte(SubOTAMin + n), nil
			}
		}
	case PartData:
		if v, ok := csvDataSubtypes[l]; ok {
			return v, nil
		}
	}
	if s == "" {
		return 0, nil
	}
	n, err := parseCSVNumber(s)
	if err != nil || n > 0xFF {
		return 0, fmt.Errorf("unknown subtype %s", s)
	}
	return byte(n), nil
}

// parseCSVNumber reads 0x10000, 65536, 64K, 1M
func parseCSVNumber(s string) (uint32, error) {
	t := strings.ToUpper(strings.TrimSpace(s))
	mult := uint64(1)
	if !strings.HasPrefix(t, "0X") {
		switch {
		case strings.HasSuffix(t, "K"):
			t, mult = t[:len(t)-1], 1<<10
		case strings.HasSuffix(t, "M"):
			t, mult = t[:len(t)-1], 1<<20
		}
	}
	v, err := strconv.ParseUint(t, 0, 32)
	if err != nil || v*mult > 1<<32-1 {
		return 0, fmt.Errorf("invalid number %q", s)
	}
	return uint32(v * mult), nil
}

// WritePartitionsCSV writes the table in the CSV form
func WritePartitionsCSV(w io.Writer, parts []Partition) error {
	if _, err := fmt.Fprintln(w, "# Name, Type, SubType, Offset, Size, Flags"); err != nil {
		return err
	}
	for _, p := range parts {
		var flags []string
		if p.Flags&FlagEncrypted != 0 {
			flags = append(flags, "encrypted")
		}
		if p.Flags&FlagReadOnly != 0 {
			flags = append(flags, "readonly")
		}
		typ := p.TypeName()
		if _, known := csvTypes[typ]; !known {
			typ = fmt.Sprintf("0x%02X", p.Type)
		}
		sub := p.SubTypeName()
		if _, err := parseSubtype(p.Type, sub); err != nil {
			sub = fmt.Sprintf("0x%02X", p.SubType)
		}
		if _, err := fmt.Fprintf(w, "%s, %s, %s, 0x%X, 0x%X, %s\n", p.Name, typ, sub, p.Offset, p.Size, strings.Join(flags, ":")); err != nil {
			return err
		}
	}
	return nil
}

// OTA data (esp_ota_select_entry_t of esp_flash_partitions.h, ESP-IDF
// master, Nov 2026): the bootloader takes the valid entry of the two
// sectors with the highest ota_seq, and boots OTA slot (ota_seq-1) mod the
// number of OTA app partitions.

// OTA image states (esp_ota_img_states_t)
const (
	OTAStateNew           = 0x0
	OTAStatePendingVerify = 0x1
	OTAStateValid         = 0x2
	OTAStateInvalid       = 0x3
	OTAStateAborted       = 0x4
	OTAStateUndefined     = 0xFFFFFFFF
	otaEntryLen           = 32
	otaEntryStateOff      = 24
	otaEntryCRCOff        = 28
	otaSectors            = 2
	otaSectorSize         = sectorSize
)

// OTASelect is what the otadata partition selects
type OTASelect struct {
	// Seq is the highest valid ota_seq, 0 when none: the bootloader then
	// boots the factory app, or the first OTA slot
	Seq uint32
	// Sector is the sector holding it
	Sector int
	State  uint32
}

// Slot is the OTA slot selected among n OTA app partitions; ok is false
// when no entry is valid
func (o OTASelect) Slot(n int) (int, bool) {
	if o.Seq == 0 || n == 0 {
		return 0, false
	}
	return int((o.Seq - 1) % uint32(n)), true
}

// ParseOTAData reads the otadata partition (two sectors)
func ParseOTAData(data []byte) OTASelect {
	var o OTASelect
	for s := 0; s < otaSectors && (s+1)*otaSectorSize <= len(data); s++ {
		e := data[s*otaSectorSize : s*otaSectorSize+otaEntryLen]
		if !OTAEntryValid(e) {
			continue
		}
		if seq := le32(e); seq > o.Seq {
			o = OTASelect{Seq: seq, Sector: s, State: le32(e[otaEntryStateOff:])}
		}
	}
	return o
}

// SelectOTA gives the sector to write and its content so that the
// bootloader boots slot of n slots next: the sequence number after the
// current one that selects the slot, in the other sector
func SelectOTA(cur OTASelect, slot, n int) (sector int, content []byte, err error) {
	if n <= 0 || slot < 0 || slot >= n {
		return 0, nil, fmt.Errorf("there is no OTA slot %d", slot)
	}
	seq := cur.Seq + 1
	for (seq-1)%uint32(n) != uint32(slot) {
		seq++
	}
	sector = 0
	if cur.Seq != 0 {
		sector = 1 - cur.Sector
	}
	content = make([]byte, otaSectorSize)
	for i := range content {
		content[i] = 0xFF
	}
	binary.LittleEndian.PutUint32(content, seq)
	binary.LittleEndian.PutUint32(content[otaEntryStateOff:], OTAStateUndefined)
	binary.LittleEndian.PutUint32(content[otaEntryCRCOff:], espCRC32(content[:4]))
	return sector, content, nil
}
