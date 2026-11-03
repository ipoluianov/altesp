package fatfs

// Names: the 8.3 names, the long names of VFAT, and how a long name gets
// its 8.3 alias (the Microsoft FAT specification 1.03, "FAT Long Directory
// Entries"; the alias as FatFs makes it: the name upper-cased, a "~N" tail
// only when characters were lost).

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
)

// checksum is the checksum of an 8.3 name that its long name entries keep
func checksum(name []byte) byte {
	var s byte
	for _, c := range name[:11] {
		s = (s&1)<<7 + s>>1 + c
	}
	return s
}

// longName decodes UTF-16 up to the zero that ends a long name
func longName(u []uint16) string {
	if i := slices.Index(u, 0); i >= 0 {
		u = u[:i]
	}
	for len(u) > 0 && u[len(u)-1] == 0xFFFF {
		u = u[:len(u)-1]
	}
	return string(utf16.Decode(u))
}

func utf16Name(s string) []uint16 { return utf16.Encode([]rune(s)) }

// shortName makes "NAME.EXT" of an 8.3 entry with the lowercase flags
// of Windows NT (byte 12), which FatFs honours too
func shortName(d []byte) string {
	b := append([]byte(nil), d[:11]...)
	if b[0] == 0x05 {
		b[0] = 0xE5 // a name that starts with E5 is kept as 05
	}
	base := strings.TrimRight(oem(b[:8]), " ")
	ext := strings.TrimRight(oem(b[8:11]), " ")
	if d[12]&0x08 != 0 {
		base = strings.ToLower(base)
	}
	if d[12]&0x10 != 0 {
		ext = strings.ToLower(ext)
	}
	if ext == "" {
		return base
	}
	return base + "." + ext
}

// cp437 is the upper half of the code page of the 8.3 names of DOS
// (ESP-IDF's default CONFIG_FATFS_CODEPAGE; Arduino-ESP32 builds with 850)
const cp437 = "ÇüéâäàåçêëèïîìÄÅÉæÆôöòûùÿÖÜ¢£¥₧ƒáíóúñÑªº¿⌐¬½¼¡«»░▒▓│┤╡╢╖╕╣║╗╝╜╛┐└┴┬├─┼╞╟╚╔╩╦╠═╬╧╨╤╥╙╘╒╓╫╪┘┌█▄▌▐▀αßΓπΣσµτΦΘΩδ∞φε∩≡±≥≤⌠⌡÷≈°∙·√ⁿ²■ "

var cp437Runes = []rune(cp437)

// oem decodes bytes of code page 437
func oem(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		if c < 0x80 {
			sb.WriteByte(c)
		} else {
			sb.WriteRune(cp437Runes[c-0x80])
		}
	}
	return sb.String()
}

// sfnChar tells the characters an 8.3 name may have, besides letters
// and digits
func sfnChar(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("$%'-_@~`!(){}^#&", c) >= 0
}

// cleanName checks a name and drops its trailing spaces and dots, as
// FatFs does
func cleanName(name string) (string, error) {
	n := strings.TrimRight(name, " .")
	if n == "" {
		return "", fmt.Errorf("%w: %q", ErrName, name)
	}
	for _, r := range n {
		if r < 0x20 || strings.ContainsRune(`"*/:<>?\|`, r) {
			return "", fmt.Errorf("%w: %q has %q", ErrName, name, r)
		}
	}
	if len(utf16Name(n)) > 255 {
		return "", fmt.Errorf("%w: longer than 255 characters", ErrName)
	}
	return n, nil
}

// makeShortName gives the 8.3 entry name of a name, whether it needs a
// long name, and the lowercase flags: "readme.txt" is README.TXT with
// both flags and no long name; "ReadMe.txt" README.TXT with a long name;
// "My Document.html" MYDOCU~1.HTM. taken tells an alias the directory
// already has.
func makeShortName(name string, taken func([]byte) bool) (short []byte, lfn bool, nt byte, err error) {
	if s, nt, ok := direct(name); ok && !taken(s) {
		return s, false, nt, nil
	}
	n := strings.TrimLeft(name, ". ")
	lossy := n != name
	b, e := n, ""
	if i := strings.LastIndexByte(n, '.'); i >= 0 {
		b, e = n[:i], n[i+1:]
	}
	conv := func(s string, max int) []byte {
		var out []byte
		for _, r := range s {
			switch {
			case r == ' ' || r == '.':
				lossy = true
			case r < 0x80 && sfnChar(byte(r)):
				c := byte(r)
				if c >= 'a' && c <= 'z' {
					c -= 'a' - 'A'
				}
				out = append(out, c)
			default:
				lossy = true
				out = append(out, '_')
			}
		}
		if len(out) > max {
			lossy = true
			out = out[:max]
		}
		return out
	}
	base, ext := conv(b, 8), conv(e, 3)
	if len(base) == 0 {
		base, lossy = []byte("_"), true
	}
	pad := func(base []byte) []byte {
		s := []byte("           ")
		copy(s, base)
		copy(s[8:], ext)
		return s
	}
	if !lossy && !taken(pad(base)) {
		return pad(base), true, 0, nil
	}
	for k := 1; k < 1000000; k++ {
		tail := "~" + strconv.Itoa(k)
		s := pad(append(slices.Clone(base[:min(len(base), 8-len(tail))]), tail...))
		if !taken(s) {
			return s, true, 0, nil
		}
	}
	return nil, false, 0, fmt.Errorf("%w: no free 8.3 alias for %q", ErrName, name)
}

// direct makes the 8.3 name of a name that is one: at most 8 and 3
// characters of the allowed ones, each part of one case
func direct(name string) ([]byte, byte, bool) {
	base, ext := name, ""
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		base, ext = name[:i], name[i+1:]
	}
	if base == "" || len(base) > 8 || len(ext) > 3 {
		return nil, 0, false
	}
	s := []byte("           ")
	var nt byte
	for k, part := range []string{base, ext} {
		upper, lower := false, false
		for i := 0; i < len(part); i++ {
			c := part[i]
			if !sfnChar(c) {
				return nil, 0, false
			}
			switch {
			case c >= 'a' && c <= 'z':
				lower = true
				c -= 'a' - 'A'
			case c >= 'A' && c <= 'Z':
				upper = true
			}
			s[k*8+i] = c
		}
		if upper && lower {
			return nil, 0, false
		}
		if lower {
			nt |= 0x08 << k
		}
	}
	return s, nt, true
}
