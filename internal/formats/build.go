package formats

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// FlashFile is a file of a build and where it goes
type FlashFile struct {
	Path string
	Addr uint32
	Kind Kind
	Name string // shown
}

// Build is what a build folder says to flash
type Build struct {
	Tool                            string // "ESP-IDF", "PlatformIO", "Arduino"
	Chip                            string // chip key like "esp32s3" when known, else ""
	Files                           []FlashFile
	ELF                             string // path when found
	FlashMode, FlashFreq, FlashSize string // as the build says, may be ""
}

// ErrNoBuild is a folder that is not a known build folder
var ErrNoBuild = errors.New("not an ESP-IDF, PlatformIO or Arduino build folder")

// Where the Arduino core and PlatformIO's Arduino framework put the files
// (arduino-esp32 platform.txt, upload.pattern_args and the merge-bin
// recipe; PlatformIO platform-espressif32 builder, Apache-2.0): the
// partition table at 0x8000, boot_app0.bin (OTA data) at 0xE000, the
// application at 0x10000, the bootloader at the chip's offset.
const (
	defaultTableAddr   = 0x8000
	defaultOTADataAddr = 0xE000
	defaultAppAddr     = 0x10000
)

// LoadBuild recognizes a build folder or a project folder containing one:
//   - ESP-IDF: build/flasher_args.json (or build/flash_args);
//   - PlatformIO: .pio/build/<env>/ with firmware.bin, bootloader.bin,
//     partitions.bin and, for Arduino, boot_app0.bin (the environment
//     built last when there are several);
//   - Arduino IDE: the sketch build folder or its exported copy
//     (sketch/build/<fqbn>/): flash_args of arduino-esp32 3.x, else the
//     separate <sketch>.ino.*.bin files, else <sketch>.ino.merged.bin.
//
// Without addresses in the build, they come from the partition table of
// the build and the chip read from the images' headers.
func LoadBuild(dir string) (*Build, error) {
	for _, d := range []string{dir, filepath.Join(dir, "build")} {
		if fileExists(filepath.Join(d, "flasher_args.json")) {
			return loadIDF(d)
		}
	}
	if b, err := loadPIO(dir); b != nil || err != nil {
		return b, err
	}
	for _, d := range arduinoDirs(dir) {
		if b, err := loadArduino(d); b != nil || err != nil {
			return b, err
		}
	}
	// An ESP-IDF build without flasher_args.json (make, older CMake)
	for _, d := range []string{dir, filepath.Join(dir, "build")} {
		if fileExists(filepath.Join(d, "flash_args")) {
			b := &Build{Tool: "ESP-IDF"}
			if err := b.flashArgs(d); err != nil {
				return nil, err
			}
			return b.finish(d)
		}
	}
	return nil, ErrNoBuild
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// flasherArgs is flasher_args.json of an ESP-IDF build
// (components/esptool_py/flasher_args.json.in, ESP-IDF master, Nov 2026)
type flasherArgs struct {
	FlashSettings struct {
		Mode string `json:"flash_mode"`
		Size string `json:"flash_size"`
		Freq string `json:"flash_freq"`
	} `json:"flash_settings"`
	FlashFiles map[string]string `json:"flash_files"`
	Extra      struct {
		Chip string `json:"chip"`
	} `json:"extra_esptool_args"`
}

func loadIDF(dir string) (*Build, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "flasher_args.json"))
	if err != nil {
		return nil, err
	}
	var fa flasherArgs
	if err := json.Unmarshal(raw, &fa); err != nil {
		return nil, fmt.Errorf("flasher_args.json: %w", err)
	}
	b := &Build{
		Tool:      "ESP-IDF",
		Chip:      fa.Extra.Chip,
		FlashMode: fa.FlashSettings.Mode,
		FlashFreq: fa.FlashSettings.Freq,
		FlashSize: fa.FlashSettings.Size,
	}
	for a, f := range fa.FlashFiles {
		addr, err := parseAddr(a)
		if err != nil {
			return nil, fmt.Errorf("flasher_args.json: address %q", a)
		}
		b.Files = append(b.Files, FlashFile{Path: filepath.Join(dir, filepath.FromSlash(f)), Addr: addr})
	}
	if len(b.Files) == 0 {
		return nil, errors.New("flasher_args.json lists no files")
	}
	// project_description.json names the ELF of the application
	var pd struct {
		AppELF string `json:"app_elf"`
		Target string `json:"target"`
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "project_description.json")); err == nil && json.Unmarshal(raw, &pd) == nil {
		if pd.AppELF != "" && fileExists(filepath.Join(dir, pd.AppELF)) {
			b.ELF = filepath.Join(dir, pd.AppELF)
		}
		if b.Chip == "" {
			b.Chip = pd.Target
		}
	}
	return b.finish(dir)
}

func parseAddr(s string) (uint32, error) {
	v, err := strconv.ParseUint(strings.TrimSpace(s), 0, 32)
	return uint32(v), err
}

// flashArgs reads a flash_args file: options of write_flash ("--flash-mode
// dio --flash-freq 80m --flash-size 4MB", old builds use "_"), then lines
// "<address> <file>" relative to the folder. ESP-IDF writes it next to
// flasher_args.json; arduino-esp32 3.x writes it into the sketch build.
func (b *Build) flashArgs(dir string) error {
	f, err := os.Open(filepath.Join(dir, "flash_args"))
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		w := strings.Fields(sc.Text())
		for i := 0; i < len(w); i++ {
			if strings.HasPrefix(w[i], "--") {
				opt := strings.ReplaceAll(strings.TrimPrefix(w[i], "--"), "_", "-")
				val := ""
				if i+1 < len(w) && !strings.HasPrefix(w[i+1], "-") {
					val = w[i+1]
					i++
				}
				switch opt {
				case "flash-mode":
					b.FlashMode = val
				case "flash-freq":
					b.FlashFreq = val
				case "flash-size":
					b.FlashSize = val
				}
				continue
			}
			addr, err := parseAddr(w[i])
			if err != nil || i+1 >= len(w) {
				continue
			}
			b.Files = append(b.Files, FlashFile{Path: filepath.Join(dir, filepath.FromSlash(w[i+1])), Addr: addr})
			i++
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if len(b.Files) == 0 {
		return errors.New("flash_args lists no files")
	}
	return nil
}

// finish reads the files: their kind by content, the chip from the image
// headers when the build didn't say it; the files are sorted by address
func (b *Build) finish(dir string) (*Build, error) {
	for i := range b.Files {
		f := &b.Files[i]
		data, err := os.ReadFile(f.Path)
		if err != nil {
			return nil, err
		}
		f.Kind = Detect(data)
		if f.Name == "" {
			f.Name = filepath.Base(f.Path)
		}
		if b.Chip == "" {
			if im, err := ParseImage(data); err == nil {
				b.Chip = ChipKey(im.ChipID)
			} else if _, im := bootloaderAt(data); im != nil {
				b.Chip = ChipKey(im.ChipID)
			}
		}
	}
	sort.Slice(b.Files, func(i, j int) bool { return b.Files[i].Addr < b.Files[j].Addr })
	return b, nil
}

// loadPIO finds .pio/build/<env>/ in a project folder, or takes dir as
// .pio/build or as the folder of an environment. Nil, nil when it is not
// a PlatformIO folder.
func loadPIO(dir string) (*Build, error) {
	env := ""
	switch {
	case fileExists(filepath.Join(dir, "firmware.bin")):
		env = dir
	default:
		for _, d := range []string{filepath.Join(dir, ".pio", "build"), dir} {
			if env = newestEnv(d); env != "" {
				break
			}
		}
	}
	if env == "" {
		return nil, nil
	}
	b := &Build{Tool: "PlatformIO"}
	firmware := filepath.Join(env, "firmware.bin")
	if fileExists(filepath.Join(env, "firmware.elf")) {
		b.ELF = filepath.Join(env, "firmware.elf")
	}
	project := filepath.Dir(filepath.Dir(filepath.Dir(env))) // <project>/.pio/build/<env>
	tableAddr := uint32(defaultTableAddr)
	if a, ok := sdkconfigTableOffset(project, filepath.Base(env)); ok {
		tableAddr = a
	}
	err := b.addSeparate(separateFiles{
		boot:     filepath.Join(env, "bootloader.bin"),
		table:    filepath.Join(env, "partitions.bin"),
		otaData:  []string{filepath.Join(env, "boot_app0.bin"), filepath.Join(env, "ota_data_initial.bin")},
		app:      firmware,
		tableAt:  tableAddr,
		toolName: "PlatformIO",
	})
	if err != nil {
		return nil, err
	}
	return b.finish(env)
}

// newestEnv is the environment under .pio/build whose firmware.bin was
// built last
func newestEnv(dir string) string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	best, bestT := "", int64(0)
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		st, err := os.Stat(filepath.Join(dir, e.Name(), "firmware.bin"))
		if err != nil {
			continue
		}
		if t := st.ModTime().UnixNano(); best == "" || t > bestT {
			best, bestT = filepath.Join(dir, e.Name()), t
		}
	}
	return best
}

// sdkconfigTableOffset reads CONFIG_PARTITION_TABLE_OFFSET from the
// sdkconfig of a PlatformIO ESP-IDF project (sdkconfig.<env>, sdkconfig)
func sdkconfigTableOffset(project, env string) (uint32, bool) {
	for _, name := range []string{"sdkconfig." + env, "sdkconfig"} {
		raw, err := os.ReadFile(filepath.Join(project, name))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "CONFIG_PARTITION_TABLE_OFFSET="); ok {
				if a, err := parseAddr(v); err == nil {
					return a, true
				}
			}
		}
	}
	return 0, false
}

// separateFiles are the files of a build without addresses
type separateFiles struct {
	boot, table string
	otaData     []string // the first that exists
	app         string
	tableAt     uint32
	toolName    string
}

// addSeparate places the files: the bootloader at the offset of the chip
// in its header, the table at tableAt, the app and the OTA data where the
// table puts them (0x10000 and 0xE000 when it says nothing)
func (b *Build) addSeparate(s separateFiles) error {
	if !fileExists(s.app) {
		return fmt.Errorf("%s build without the application image", s.toolName)
	}
	appAddr, otaAddr := uint32(defaultAppAddr), uint32(defaultOTADataAddr)
	if raw, err := os.ReadFile(s.table); err == nil {
		parts, err := ParsePartitionTable(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(s.table), err)
		}
		if p, ok := AppPartition(parts); ok {
			appAddr = p.Offset
		}
		if p, ok := FindPartition(parts, PartData, SubDataOTA); ok {
			otaAddr = p.Offset
		}
		b.Files = append(b.Files, FlashFile{Path: s.table, Addr: s.tableAt})
	}
	if raw, err := os.ReadFile(s.boot); err == nil {
		im, err := ParseImage(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(s.boot), err)
		}
		c, ok := chipByID(im.ChipID)
		if !ok {
			return fmt.Errorf("%s: unknown chip", filepath.Base(s.boot))
		}
		b.Chip = c.key
		b.Files = append(b.Files, FlashFile{Path: s.boot, Addr: c.boot})
	}
	for _, p := range s.otaData {
		if fileExists(p) {
			b.Files = append(b.Files, FlashFile{Path: p, Addr: otaAddr})
			break
		}
	}
	b.Files = append(b.Files, FlashFile{Path: s.app, Addr: appAddr})
	return nil
}

// arduinoDirs are the folders an Arduino build may be in: the folder
// itself and the exported copies sketch/build/<fqbn>/
func arduinoDirs(dir string) []string {
	dirs := []string{dir}
	ents, _ := os.ReadDir(filepath.Join(dir, "build"))
	for _, e := range ents {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(dir, "build", e.Name()))
		}
	}
	return dirs
}

// loadArduino reads the output of an Arduino IDE sketch build. Nil, nil
// when the folder has no sketch binaries.
func loadArduino(dir string) (*Build, error) {
	apps, _ := filepath.Glob(filepath.Join(dir, "*.ino.bin"))
	merged, _ := filepath.Glob(filepath.Join(dir, "*.ino.merged.bin"))
	if len(apps) == 0 && len(merged) == 0 {
		return nil, nil
	}
	b := &Build{Tool: "Arduino"}
	stem := ""
	if len(apps) > 0 {
		stem = strings.TrimSuffix(apps[0], ".bin") // <dir>/<sketch>.ino
	} else {
		stem = strings.TrimSuffix(merged[0], ".merged.bin")
	}
	if fileExists(stem + ".elf") {
		b.ELF = stem + ".elf"
	}
	if fileExists(filepath.Join(dir, "flash_args")) {
		if err := b.flashArgs(dir); err == nil {
			if allExist(b.Files) {
				return b.finish(dir)
			}
		}
		b.Files = nil
	}
	// The separate files when they are all there, else the merged image
	boot, table := stem+".bootloader.bin", stem+".partitions.bin"
	if len(apps) > 0 && fileExists(boot) && fileExists(table) {
		err := b.addSeparate(separateFiles{
			boot:     boot,
			table:    table,
			otaData:  []string{filepath.Join(dir, "boot_app0.bin")},
			app:      apps[0],
			tableAt:  defaultTableAddr,
			toolName: "Arduino",
		})
		if err != nil {
			return nil, err
		}
		return b.finish(dir)
	}
	if len(merged) > 0 {
		raw, err := os.ReadFile(merged[0])
		if err != nil {
			return nil, err
		}
		b.Files = []FlashFile{{Path: merged[0], Addr: MergedAddr(raw)}}
		return b.finish(dir)
	}
	return nil, fmt.Errorf("Arduino build without the bootloader and the partition table: %s", filepath.Base(apps[0]))
}

func allExist(files []FlashFile) bool {
	for _, f := range files {
		if !fileExists(f.Path) {
			return false
		}
	}
	return true
}
