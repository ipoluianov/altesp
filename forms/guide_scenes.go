package forms

import (
	"image/color"
	"math"
	"strings"

	"github.com/ipoluianov/nui/ui"
)

// The pictures of the guide's topics; each loops

// drawChip: the blocks inside the chip and the flash beside it; the code
// comes from the flash through the cache to the cores
func drawChip(s *scene) {
	blue, green, orange, purple, grey := colorInfo.get(), colorOK.get(), colorWarning.get(), colorDecoded.get(), colorSecondary.get()
	s.frame(20, 20, 400, 262, blue, G().ChipBox)
	cpu := 0.15 + 0.85*s.pulse(1.2)
	s.box(40, 50, 100, 58, mix(blue, s.pal.Base, cpu), G().CPU)
	s.box(155, 50, 80, 58, grey, "ROM")
	s.box(250, 50, 80, 58, green, "SRAM")
	s.box(345, 50, 60, 58, orange, "eFuse")
	s.box(40, 125, 250, 48, purple, G().Radio)
	s.box(305, 125, 100, 48, blue, G().Cache)
	s.box(40, 190, 365, 72, grey, G().Peripherals)

	s.box(470, 55, 150, 75, green, G().FlashChip)
	s.box(470, 170, 150, 60, grey, G().PSRAM)
	bus := mix(s.pal.Text, s.pal.Base, 0.4)
	s.line(405, 149, 450, 149, bus, 2)
	s.line(450, 92, 450, 200, bus, 2)
	s.line(450, 92, 470, 92, bus, 2)
	s.line(450, 200, 470, 200, bus, 2)
	s.text(452, 136, 60, 12, "SPI", grey, ui.HAlignLeft, false)

	// Code: from the flash through the cache into the cores
	path := [][2]float64{{470, 92}, {450, 92}, {450, 149}, {405, 149}, {355, 149}, {355, 118}, {90, 118}, {90, 108}}
	for i := 0; i < 4; i++ {
		p := math.Mod(s.phase(2.4)+float64(i)*0.25, 1)
		x, y := along(path, p)
		s.dot(x, y, 4, blue)
	}
}

// drawMemory: SRAM forgets without power, the flash keeps
func drawMemory(s *scene) {
	blue, green, grey, red := colorInfo.get(), colorOK.get(), colorSecondary.get(), colorError.get()
	on := s.phase(6*guidePace) < 0.6
	power := G().PowerOn
	pcol := green
	if !on {
		power, pcol = G().PowerOff, red
	}
	s.box(250, 14, 140, 34, pcol, power)

	cells := func(x0 float64, col func(i int) (fill bool)) {
		for r := 0; r < 5; r++ {
			for c := 0; c < 10; c++ {
				i := r*10 + c
				x, y := x0+float64(c)*25, 110+float64(r)*25
				fillCol := s.tint(grey, 0.1)
				if col(i) {
					fillCol = s.tint(blue, 0.55+0.3*float64((i*7)%3)/2)
				}
				s.cnv.FillFrame(s.X(x), s.Y(y), s.L(21), s.L(21), s.L(3), fillCol, mix(grey, s.pal.Base, 0.4))
			}
		}
	}
	s.frame(20, 66, 270, 230, blue, G().RAMBox)
	s.text(30, 238, 250, 50, G().RAMNote, s.dim(), ui.HAlignLeft, false)
	// SRAM fills while on, empties at once when off
	filled := 0
	if on {
		filled = int(math.Min(50, s.phase(6*guidePace)/0.6*80))
	}
	cells(35, func(i int) bool { return i < filled })

	s.frame(350, 66, 270, 230, green, G().FlashBox)
	s.text(360, 238, 250, 50, G().FlashNote, s.dim(), ui.HAlignLeft, false)
	cells(365, func(i int) bool { return i%7 != 3 && i < 44 })
}

// drawBoot: from the ROM to the application, or into download mode
func drawBoot(s *scene) {
	blue, green, orange, grey := colorInfo.get(), colorOK.get(), colorWarning.get(), colorSecondary.get()
	steps := []struct {
		label, addr string
	}{
		{G().ROMStep, G().InChip},
		{G().BootloaderStep, "0x0 … 0x2000"},
		{G().TableStep, "0x8000"},
		{G().AppStep, "factory, ota_N"},
	}
	cur, within := s.step(6, 1.3)
	download := cur >= 4
	for i, st := range steps {
		x := 20 + float64(i)*155
		col := grey
		switch {
		case download && i == 0:
			col = blue
		case !download && i == cur:
			col = blue
		case !download && i < cur:
			col = green
		}
		s.box(x, 40, 135, 70, col, st.label)
		s.text(x, 115, 135, 20, st.addr, s.dim(), ui.HAlignCenter, false)
		if i > 0 {
			c := grey
			if !download && i <= cur {
				c = green
			}
			s.arrow(x-18, 75, x-2, 75, c)
		}
	}
	// Moving along the steps
	if !download {
		x := 20 + float64(cur)*155 + 135*within
		s.dot(x, 30, 5, blue)
	}
	// The BOOT button held: download mode
	col := grey
	if download {
		col = orange
	}
	s.arrow(87, 140, 87, 190, col)
	s.box(20, 190, 290, 80, col, G().DownloadStep)
	s.text(330, 190, 290, 80, G().BootPin, s.dim(), ui.HAlignLeft, false)
	if download && int(s.t*3)%2 == 0 {
		s.dot(30, 200, 5, orange)
	}
}

// flashAreas are the parts of a usual flash map: the name, the share of the
// bar it is drawn with, the address, the kind
var flashAreas = []struct {
	name, addr string
	w          float64
	kind       int // 0 boot, 1 table, 2 data, 3 app
}{
	{"bootloader", "0x1000", 92, 0},
	{"table", "0x8000", 58, 1},
	{"nvs", "0x9000", 52, 2},
	{"otadata", "0xF000", 74, 2},
	{"phy", "0x11000", 46, 2},
	{"ota_0", "0x20000", 104, 3},
	{"ota_1", "0x1A0000", 104, 3},
	{"storage", "0x320000", 70, 2},
}

// drawFlashMap: the flash bar and the partition table, each area shown in
// turn
func drawFlashMap(s *scene) {
	blue, green, orange, purple := colorInfo.get(), colorOK.get(), colorWarning.get(), colorDecoded.get()
	kindCol := []color.RGBA{purple, orange, green, blue}
	cur, _ := s.step(len(flashAreas), 1.6)
	x := 20.0
	for i, a := range flashAreas {
		col := kindCol[a.kind]
		alpha := 0.3
		if i == cur {
			alpha = 0.75
		}
		s.cnv.FillFrame(s.X(x), s.Y(30), s.L(a.w-2), s.L(54), s.L(4), s.tint(col, alpha), mix(col, s.pal.Base, 0.8))
		s.text(x, 30, a.w-2, 54, a.name, s.fg(), ui.HAlignCenter, i == cur)
		// The addresses take two rows: the areas are narrow
		ay := 86.0
		if i%2 == 1 {
			ay = 102
		}
		s.text(x, ay, 90, 16, a.addr, s.dim(), ui.HAlignLeft, false)
		x += a.w
	}
	s.text(20, 8, 300, 18, "0x0", s.dim(), ui.HAlignLeft, false)
	s.text(320, 8, 300, 18, "4 MB", s.dim(), ui.HAlignRight, false)
	// What the area is
	a := flashAreas[cur]
	s.frame(20, 130, 600, 160, kindCol[a.kind], a.name)
	s.text(36, 156, 568, 120, G().AreaText[cur], s.fg(), ui.HAlignLeft, false)
}

// drawNVS: values written one after another into a page; a new value of a
// key marks the old one erased; a full page is moved to a fresh one
func drawNVS(s *scene) {
	blue, green, grey, orange := colorInfo.get(), colorOK.get(), colorSecondary.get(), colorWarning.get()
	writes := []string{"wifi.ssid = \"home\"", "wifi.pass = \"••••••\"", "boot.count = 1", "boot.count = 2", "boot.count = 3", "app.mode = 1", "boot.count = 4", "boot.count = 5"}
	const slots = 8
	cur, within := s.step(len(writes)+3, 1.1)
	n := min(cur+1, len(writes))
	// The last write of each key is live
	live := map[string]int{}
	for i := 0; i < n; i++ {
		key := writes[i][:strings.Index(writes[i], " ")]
		live[key] = i
	}
	moving := cur >= len(writes)
	s.frame(20, 52, 290, 238, blue, G().Page+" 1")
	s.frame(330, 52, 290, 238, green, G().Page+" 2")
	for i := 0; i < slots; i++ {
		y := 80 + float64(i)*25
		key := ""
		if i < n {
			key = writes[i][:strings.Index(writes[i], " ")]
		}
		switch {
		case i >= n:
			s.cnv.FillFrame(s.X(32), s.Y(y), s.L(266), s.L(21), s.L(3), s.tint(grey, 0.06), mix(grey, s.pal.Base, 0.3))
		case live[key] == i:
			col := blue
			if i == n-1 && !moving {
				col = orange
			}
			s.cnv.FillFrame(s.X(32), s.Y(y), s.L(266), s.L(21), s.L(3), s.tint(col, 0.3+0.3*(1-within)*b2f(i == n-1 && !moving)), mix(col, s.pal.Base, 0.6))
			s.mono(40, y, 256, 21, writes[i], s.fg())
		default:
			s.cnv.FillFrame(s.X(32), s.Y(y), s.L(266), s.L(21), s.L(3), s.tint(grey, 0.12), mix(grey, s.pal.Base, 0.4))
			s.mono(40, y, 256, 21, writes[i], s.dim())
			s.line(40, y+11, 290, y+11, s.dim(), 1)
		}
	}
	// The page is full: the live values go to page 2
	if moving {
		k := 0
		shown := (cur - len(writes) + 1) * 2
		for i := 0; i < len(writes); i++ {
			key := writes[i][:strings.Index(writes[i], " ")]
			if live[key] != i || k >= shown {
				continue
			}
			y := 80 + float64(k)*25
			s.cnv.FillFrame(s.X(342), s.Y(y), s.L(266), s.L(21), s.L(3), s.tint(green, 0.35), mix(green, s.pal.Base, 0.6))
			s.mono(350, y, 256, 21, writes[i], s.fg())
			k++
		}
		s.arrow(300, 170, 340, 170, green)
	}
	note := G().NVSWrite
	if moving {
		note = G().NVSMove
	}
	// Two lines may be needed: the pages start below them
	s.text(20, 2, 600, 46, note, s.dim(), ui.HAlignLeft, false)
}

// drawOTA: the new version downloaded into the other slot, otadata
// switched, the restart into it
func drawOTA(s *scene) {
	blue, green, orange, grey := colorInfo.get(), colorOK.get(), colorWarning.get(), colorSecondary.get()
	cur, within := s.step(4, 2)
	slot := func(x float64, name, version string, runs bool, progress float64) {
		col := grey
		if runs {
			col = green
		}
		s.frame(x, 60, 240, 140, col, name)
		if version != "" {
			s.text(x, 90, 240, 40, version, s.fg(), ui.HAlignCenter, true)
		}
		if runs {
			s.text(x, 140, 240, 30, "▶ "+G().Runs, green, ui.HAlignCenter, false)
		}
		if progress > 0 && progress < 1 {
			s.cnv.FillFrame(s.X(x+20), s.Y(165), s.L(200), s.L(14), s.L(4), s.tint(grey, 0.1), mix(grey, s.pal.Base, 0.4))
			s.cnv.FillRect(s.X(x+22), s.Y(167), s.L(196*progress), s.L(10), blue)
			s.text(x, 140, 240, 24, G().Downloading, blue, ui.HAlignCenter, false)
		}
	}
	v0, v1 := "v1.0", ""
	run0, run1 := true, false
	prog := 0.0
	switch cur {
	case 0:
		prog = within
		if within > 0.05 {
			v1 = "v1.1"
		}
	case 1:
		v1 = "v1.1"
	case 2, 3:
		v1 = "v1.1"
		run0, run1 = false, true
	}
	slot(20, "ota_0", v0, run0, 0)
	slot(380, "ota_1", v1, run1, prog)
	// otadata points to the slot that boots
	target := 140.0
	if cur >= 1 {
		target = 500
	}
	col := orange
	s.box(250, 230, 140, 50, col, "otadata")
	s.arrow(320, 230, target, 202, col)
	notes := []string{G().OTADownload, G().OTASwitch, G().OTARestart, G().OTARollback}
	s.text(20, 8, 600, 40, notes[cur], s.dim(), ui.HAlignLeft, false)
	if cur == 2 && int(s.t*4)%2 == 0 {
		s.dot(500, 50, 5, blue)
	}
}

// drawFS: files in a data partition, for the three file systems
func drawFS(s *scene) {
	blue, green, orange, grey := colorInfo.get(), colorOK.get(), colorWarning.get(), colorSecondary.get()
	kinds := []struct {
		name, note string
		col        color.RGBA
	}{
		{"LittleFS", G().LittleFSNote, green},
		{"SPIFFS", G().SPIFFSNote, orange},
		{"FAT", G().FATNote, blue},
	}
	cur, within := s.step(3, 3)
	k := kinds[cur]
	s.frame(20, 20, 330, 270, k.col, "storage · "+k.name)
	files := []string{"config.json", "index.html", "style.css", "log.txt"}
	if cur == 2 {
		files = []string{"www/index.html", "www/style.css", "config.json", "log.txt"}
	}
	for i, f := range files {
		appear := math.Min(1, math.Max(0, within*5-float64(i)*0.7))
		if appear <= 0 {
			continue
		}
		y := 60 + float64(i)*52
		x := 40 + (1-appear)*-30
		s.cnv.FillFrame(s.X(x), s.Y(y), s.L(36), s.L(42), s.L(3), s.tint(grey, 0.15), mix(grey, s.pal.Base, 0.6))
		s.line(x+8, y+14, x+28, y+14, s.dim(), 1)
		s.line(x+8, y+22, x+28, y+22, s.dim(), 1)
		s.line(x+8, y+30, x+22, y+30, s.dim(), 1)
		s.text(x+48, y, 240, 42, f, mix(s.fg(), s.pal.Base, appear), ui.HAlignLeft, false)
	}
	s.text(370, 20, 250, 270, k.note, s.fg(), ui.HAlignLeft, false)
}

// drawEFuse: bits burned from 0 to 1, never back
func drawEFuse(s *scene) {
	orange, grey, red := colorWarning.get(), colorSecondary.get(), colorError.get()
	const bits = 16
	burned := []int{1, 4, 5, 9, 12, 14}
	cur, within := s.step(len(burned)+2, 0.9)
	s.frame(20, 30, 600, 120, orange, "eFuse")
	for i := 0; i < bits; i++ {
		x := 40 + float64(i)*36
		on := false
		for k, b := range burned {
			if b == i && k < cur {
				on = true
			}
		}
		fill := s.tint(grey, 0.1)
		label := "0"
		if on {
			fill, label = s.tint(orange, 0.7), "1"
		}
		s.cnv.FillFrame(s.X(x), s.Y(70), s.L(30), s.L(40), s.L(3), fill, mix(grey, s.pal.Base, 0.5))
		s.mono(x+10, 70, 20, 40, label, s.fg())
	}
	// The spark of the bit being burned
	if cur < len(burned) {
		x := 40 + float64(burned[cur])*36 + 15
		s.dot(x, 60-12*(1-within), 4+3*within, orange)
	}
	// 1 → 0 can't be done
	s.box(150, 190, 120, 60, orange, "1")
	s.box(370, 190, 120, 60, grey, "0")
	s.arrow(275, 220, 365, 220, red)
	s.line(305, 200, 335, 240, red, 3)
	s.line(335, 200, 305, 240, red, 3)
	s.text(20, 260, 600, 30, G().EFuseOnce, s.dim(), ui.HAlignCenter, false)
}

// drawSecurity: the flash encrypted with a key of the chip; the bootloader
// runs only what is signed
func drawSecurity(s *scene) {
	blue, green, orange, red, grey := colorInfo.get(), colorOK.get(), colorWarning.get(), colorError.get(), colorSecondary.get()
	// Flash encryption
	s.frame(20, 20, 290, 270, blue, G().EncryptionBox)
	plain := "WIFI=home PASS=secret12"
	enc := "9f 3a c1 07 e2 5b 88 d4 1c 6e"
	p := s.pulse(4)
	s.box(40, 60, 250, 50, green, G().InChipPlain+"\n"+plain)
	s.box(110, 150, 110, 40, orange, G().KeyInEFuse)
	s.arrow(165, 112, 165, 148, grey)
	s.arrow(165, 192, 165, 222, grey)
	s.box(40, 222, 250, 50, mix(red, s.pal.Base, 0.5+0.5*p), G().InFlashEnc+"\n"+enc)

	// Secure Boot
	s.frame(330, 20, 290, 270, green, "Secure Boot")
	cur, _ := s.step(2, 2.5)
	signed := cur == 0
	img := G().SignedImage
	col := green
	if !signed {
		img, col = G().ChangedImage, red
	}
	s.box(350, 60, 250, 50, col, img)
	s.arrow(475, 112, 475, 148, grey)
	s.box(350, 150, 250, 50, blue, G().BootloaderChecks)
	s.arrow(475, 202, 475, 222, grey)
	res := "✔ " + G().Starts
	if !signed {
		res = "✘ " + G().Refused
	}
	s.box(350, 222, 250, 50, col, res)
}

// drawUSB: through a bridge the port stays on a reset; on the chip's own
// USB it goes and comes back
func drawUSB(s *scene) {
	blue, green, orange, red, grey := colorInfo.get(), colorOK.get(), colorWarning.get(), colorError.get(), colorSecondary.get()
	reset := s.phase(4*guidePace) > 0.7
	// The bridge
	s.text(20, 4, 600, 20, G().ViaBridge, s.dim(), ui.HAlignLeft, true)
	s.box(20, 30, 110, 60, grey, G().Computer)
	s.box(220, 30, 130, 60, green, "CP2102 / CH340")
	chip := blue
	if reset {
		chip = orange
	}
	s.box(470, 30, 150, 110, chip, "ESP32")
	s.line(130, 60, 220, 60, green, 3)
	s.text(140, 40, 70, 16, "USB", s.dim(), ui.HAlignCenter, false)
	s.line(350, 50, 470, 50, blue, 2)
	s.text(360, 34, 110, 16, "TX / RX", s.dim(), ui.HAlignCenter, false)
	ctl := grey
	if reset {
		ctl = orange
	}
	s.line(350, 80, 470, 80, ctl, 2)
	s.line(350, 110, 470, 110, ctl, 2)
	s.text(360, 84, 110, 16, "RTS → EN", s.dim(), ui.HAlignCenter, false)
	s.text(360, 114, 110, 16, "DTR → BOOT", s.dim(), ui.HAlignCenter, false)
	if !reset {
		x, y := along([][2]float64{{130, 60}, {220, 60}, {285, 60}, {285, 50}, {470, 50}}, s.phase(1))
		s.dot(x, y, 4, blue)
	}

	// The chip's own USB
	s.text(20, 160, 600, 20, G().ViaOwnUSB, s.dim(), ui.HAlignLeft, true)
	s.box(20, 190, 110, 60, grey, G().Computer)
	s.box(470, 180, 150, 90, chip, "ESP32-C3 / C6 / S3\n"+G().USBInside)
	if reset {
		if int(s.t*6)%2 == 0 {
			s.line(130, 220, 470, 220, red, 3)
		}
		s.text(150, 228, 300, 40, G().PortGone, red, ui.HAlignCenter, false)
	} else {
		s.line(130, 220, 470, 220, green, 3)
		x, _ := along([][2]float64{{130, 220}, {470, 220}}, s.phase(1))
		s.dot(x, 220, 4, blue)
	}
	if reset {
		s.text(20, 275, 600, 20, G().Resetting, orange, ui.HAlignCenter, false)
	}
}

// familyRows are the chips of the family: core, radio, USB
var familyRows = [][4]string{
	{"ESP32", "2× Xtensa LX6, 240 MHz", "Wi-Fi, BT, BLE", "-"},
	{"ESP32-S2", "1× Xtensa LX7, 240 MHz", "Wi-Fi", "OTG"},
	{"ESP32-S3", "2× Xtensa LX7, 240 MHz", "Wi-Fi, BLE 5", "OTG, Serial/JTAG"},
	{"ESP32-C2", "1× RISC-V, 120 MHz", "Wi-Fi, BLE 5", "-"},
	{"ESP32-C3", "1× RISC-V, 160 MHz", "Wi-Fi, BLE 5", "Serial/JTAG"},
	{"ESP32-C5", "1× RISC-V, 240 MHz", "Wi-Fi 6 2.4/5 GHz, BLE 5, 802.15.4", "Serial/JTAG"},
	{"ESP32-C6", "1× RISC-V, 160 MHz", "Wi-Fi 6, BLE 5, 802.15.4", "Serial/JTAG"},
	{"ESP32-C61", "1× RISC-V, 160 MHz", "Wi-Fi 6, BLE 5", "Serial/JTAG"},
	{"ESP32-H2", "1× RISC-V, 96 MHz", "BLE 5, 802.15.4", "Serial/JTAG"},
	{"ESP32-P4", "2× RISC-V, 400 MHz", "-", "OTG HS, Serial/JTAG"},
}

// drawFamily: the table of the chips, a row lit in turn
func drawFamily(s *scene) {
	blue, purple := colorInfo.get(), colorDecoded.get()
	s.small = true
	cols := []float64{10, 100, 265, 500}
	widths := []float64{90, 165, 235, 130}
	head := []string{G().ColChip, G().ColCore, G().ColRadio, "USB"}
	for i, h := range head {
		s.text(cols[i]+4, 6, widths[i], 22, h, s.fg(), ui.HAlignLeft, true)
	}
	cur, _ := s.step(len(familyRows), 1.2)
	for r, row := range familyRows {
		y := 30 + float64(r)*26.5
		col := blue
		if row[1][3] == 'X' {
			col = purple
		}
		alpha := 0.06
		if r == cur {
			alpha = 0.35
		}
		s.cnv.FillFrame(s.X(10), s.Y(y), s.L(620), s.L(24), s.L(3), s.tint(col, alpha), mix(col, s.pal.Base, 0.3))
		for i, v := range row {
			if v == "-" {
				v = "—"
			}
			s.text(cols[i]+4, y, widths[i]-6, 24, v, s.fg(), ui.HAlignLeft, i == 0)
		}
	}
}

// drawPanic: the crash log, the .elf, the function and line
func drawPanic(s *scene) {
	blue, red, purple, grey := colorInfo.get(), colorError.get(), colorDecoded.get(), colorSecondary.get()
	lines := []string{
		"Guru Meditation Error: Core 0 panic'ed",
		"(LoadProhibited). Exception was unhandled.",
		"",
		"Backtrace: 0x400d1f2a:0x3ffb1e40",
		"           0x400d2b10:0x3ffb1e60",
	}
	cur, within := s.step(4, 1.5)
	s.frame(20, 20, 430, 160, red, G().LogBox)
	shown := len(lines)
	if cur == 0 {
		shown = int(within * float64(len(lines)+1))
	}
	for i := 0; i < min(shown, len(lines)); i++ {
		s.mono(32, 46+float64(i)*24, 414, 22, lines[i], red)
	}
	s.box(480, 60, 140, 70, grey, "app.elf\n"+G().SameBuild)
	if cur >= 1 {
		s.arrow(450, 120, 476, 100, blue)
	}
	if cur >= 2 {
		s.frame(20, 200, 600, 90, purple, G().Decoded)
		s.mono(36, 226, 580, 22, "0x400d1f2a: read_sensor at main/sensor.c:42", purple)
		if cur >= 3 {
			s.mono(36, 252, 580, 22, "0x400d2b10: app_main at main/main.c:17", purple)
		}
		s.arrow(550, 132, 550, 196, blue)
	}
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
