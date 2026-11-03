package forms

import (
	"github.com/ipoluianov/nui/ui"
	"github.com/ipoluianov/nui/ui/i18n"
)

// Strings are the texts of the interface in one language
type Strings struct {
	// Top bar
	PortTip, NoPorts, Connect, ConnectTip    string
	RefreshPortsTip, PortsFound              string
	SectorErased, SectorData, SectorChecking string
	BoardUnplugged                           string
	Disconnect, Cancel                       string
	Tools, MenuTip                           string
	ManualHint                               string

	// Board card and recent projects
	Board, NoBoard, NoBoardHint string
	RecentProjects, NoProjects  string
	FlashLabel, Crystal         string
	OnBoard                     string
	// The card of a board without firmware to boot
	NoFirmware, IncompleteFirmware, EmptyFlash string
	EmptyLoopUSB, EmptyLoopUART, DropToFlash   string
	NoPartitionTable, NoApplication            string

	// Card menu
	CopyMAC, CopyAll, SaveReport, ResetBoard, EnterBootloader string
	Copied, Saved, TextFiles, AlreadyDownload                 string

	// Tabs
	TabFlash, TabMemory, TabPartitions string

	// Partition editors (NVS, file systems)
	WriteToBoard, Changed, DropChanges, DropChangesAsk           string
	NVSv1, NVSv2, NVSUsage, NVSEncrypted, DecryptWith, KeyFile   string
	AllNamespaces, ColNamespace, ColKey, ColValue                string
	AddKey, ChangeValue, AddNamespace, NamespacePrompt           string
	Delete, DeleteNamespace, DeleteKeyAsk, DeleteNSAsk           string
	ExportNamespace, SaveNVSImage, NVSImported, NVSValueHint     string
	BadBlob, BadNumber, WriteNVSAsk                              string
	Up, AddFiles, AddFolder, NewFolder, SaveFSImage, EmptyFolder string
	Download, Downloaded, Replace, Rename, NewName               string
	DeleteFileAsk, SPIFFSFolders, FilesAdded, WriteFSAsk         string
	File, Folder                                                 string

	// Monitor: panics
	SetELF, SetELFTip, NoELF, ELFShown, ELFError, ELFMismatch   string
	CopyLine, DecodeAddress, NeedELF, NotInELF                  string
	FilterTag, FilterLevel, FilteredTag, FilteredLevel, ShowAll string

	// Tools
	CoredumpAsk, CoredumpNoELF, CoredumpMismatch, FromBoard    string
	FromFile, Exception, Crashed, Priority, NoFrames           string
	GenKind, GenNVS, GenLittleFS, GenSPIFFS, GenFAT, GenMerged string
	GenSource, GenVersion, Browse, GenSave, GenAddToList       string
	GenNVSInfo, GenFSInfo, GenMergedInfo                       string
	EditorInfo, AddRow, RemoveRow, DefaultTable, ColFlags      string
	OffsetAuto, Encrypted, TableOK, SaveCSV, SaveBinary        string
	CopyText, SaveText                                         string

	// The log of operations
	OperationsLog, NoOperations, OpFailed                 string
	OpEraseAll, OpEraseSectors, OpWriteBytes, OpWritePart string
	OpFlash, OpNote                                       string
	OpErasePart, OpOTA, OpTable, OpWriteNVS, OpWriteFS    string

	// eFuse
	ShowEFuse, EFuseFilter, HideZero, WriteMode, WriteModeBanner string
	Coding, ColDescription, PendingChanges, Review, ClearChanges string
	FieldProtected, EFusePrompt, BadFuseValue                    string
	FuseTooLarge, ConnectFirst, BurnRefused, NothingToBurn       string
	BurnChanges, BurnBlocks, BurnIrreversible, TypeBurn, Burn    string
	Burning, BurnResult, Burned, Skipped, CodingError, Attempts  string
	ReconnectNoStub, ReconnectNoStubAsk, OpBurn                  string
	KeyPrompt, KeyNow, KeyPreview, KeyLength, KeyBytes           string

	// Settings, keys, the port menu
	Settings, SettingsTitle, SystemLanguage, DefaultBaud          string
	Shortcuts, KeyConnect, KeyFind, FindNext, FindPrev, SearchLog string
	ResetSequence, ResetAuto, ResetBridge, ResetUSBJTAG           string
	ResetManual, ResetNext, Reconnect, AdapterProps               string

	// Flash options, Show in hex, updates
	PortBaud, PortBaudPrompt, PortBaudNote, ParamsOverride        string
	AsInFile, FlashMode, FlashFreq, FlashSizeLabel, ParamsNote    string
	OverrideShown, EncryptWrite, EncryptNote                      string
	CheckUpdates, CheckUpdatesTitle, CheckingUpdates, UpdateError string
	UpdateAvailable, OpenReleasePage, UpToDate                    string

	// Images without a board
	OpenImage, BackToBoard, EmptyFile, ImageOpened, ImageShown string

	// Memory tab
	MemoryHint, NoReadSecure, NoReadStub                      string
	GoTo, GoToAddr, GoToPrompt, PastEnd                       string
	Find, FindMenu, FindPrompt, FindEmpty, FindReadAll        string
	ReadWhole, FindInRead, FoundAt, NotFound                  string
	SaveRegion, SaveSelection, AddressLabel, SizeLabel        string
	CompareFile, Comparing, Identical, DiffFound              string
	Differences, CloseDiff                                    string
	CopyHex, CopyC, TooLargeToCopy, NotReadYet                string
	WriteFileHere, WriteHereAsk, WrittenAt, Write             string
	EraseSectors, EraseSectorsAsk, ErasedAt                   string
	EnableEditing, EditingHint                                string
	WriteChanges, DiscardChanges, WriteEditsAsk, EditsWritten string
	BackupFirst, Affected, Bootloader, PartitionTable         string
	SelectedFmt                                               string

	// Partitions tab
	PartitionsHint, NoTableBoard, NoTableImage, TableAt    string
	ColName, ColType, ColSubtype, ColApp, Boots, EmptyApp  string
	Refresh, ExportCSV, ImportCSV                          string
	SavePartition, WritePartition, ErasePartition          string
	MakeActive, OpenInHex, NowBoots                        string
	EraseRest, WritePartAsk, TooLargeForPart, PartWritten  string
	ErasePartAsk, PartErased                               string
	TableProblems, WriteTableAsk, WriteTable, TableWritten string
	AppProject, AppVersion, AppBuilt, AppSecureVersion     string
	AppELF, AppBoots                                       string

	// Flash tab
	DropHint, NoPortsHint                 string
	ColFile, ColAddress, ColSize, ColKind string
	KindUnknown, Missing                  string
	// Kind is the kind of a file (formats.Kind) in the language
	Kind                                             func(kind string) string
	Add, EraseAll, ReadFlash, FlashBtn, Stop         string
	ReadFlashTitle, ExportWhole, ExportParts         string
	ExportTrimmed, ExportApp, ExportUserData         string
	ExportFolderNote, ExportTrimmedNote              string
	ExportAppNote, ExportEncrypted, ExportDone       string
	SaveBtn, OpenFolder                              string
	BuildFolderBtn, BuildFolderTip, BuildFolderTitle string
	AddFilesLink, BuildFolderLink                    string
	Verify, ResetAfter, FlashOptions                 string
	OnlyChanged, NoCompress, NoStub, NoStubNext      string
	NoReset, MonitorAfter                            string
	ChangeAddress, SkipFile, RemoveFile              string
	AddressTitle, AddressPrompt                      string
	Firmware, AllFiles, NotABuild, BuildLoaded       string
	NothingToFlash, Connecting, Writing              string
	WritingFile, Flashed, PartialWrite               string
	WarnProblems, FatalProblems                      string
	Continue, FlashAnyway                            string
	EraseAsk, EraseUnknown, EraseBootloader          string
	ErasePartitions, EraseApp, ReadFirst, Erase      string
	Erasing, Erased, Reading, ReadingProgress        string
	ReadDone                                         string

	// Monitor tab
	BaudTip, BadBaud, ResetBtn, ResetTip       string
	MonitorBtn, MonitorTip, MonitorTitle       string
	CloseMonitorTip, MonSession                string
	Pause, Resume, Clear, SaveLog              string
	SendHint, Send, EndingTip, NoEnding        string
	MonOpening, MonOpen, MonWaiting, MonClosed string
	MonJTAG, MonNotOpen                        string

	// Tools menu
	ToolEfuse, ToolPartitionEditor, ToolImages, ToolCoredump string

	// State
	NotConnected, NoPortSelected, Busy, Stopped string
	Connected, Monitoring, BaudFmt              string
	StubLoader, ROMLoader                       string

	// Main menu
	Theme, ThemeDark, ThemeLight, Language string
	About, AboutText, LicenseText          string
	Trademark, StubCredit                  string

	// Installation
	InstallLink, UpdateLink, UninstallLink, Installed string
	InstallAsk, InstallFailed, UninstallAsk           string
	Uninstalled                                       string
}

var english = Strings{
	RefreshPortsTip: "Look for the ports again: an unplugged board leaves the list",
	PortsFound:      "Ports found: %d",
	SectorErased:    "erased (FF)",
	SectorData:      "data",
	SectorChecking:  "being checked",
	BoardUnplugged:  "The board was unplugged",
	PortTip:         "The port of the board",
	NoPorts:         "No ports",
	Connect:         "Connect",
	ConnectTip:      "Reset the board into download mode and read the chip",
	Tools:           "Tools",
	MenuTip:         "Menu",
	Disconnect:      "Disconnect",
	Cancel:          "Cancel",
	ManualHint:      "Put the board into download mode: hold BOOT, press and release RESET, release BOOT",

	FlashLabel: "Flash",
	Crystal:    "Crystal",
	OnBoard:    "On board:",

	NoFirmware:         "No firmware",
	IncompleteFirmware: "Incomplete firmware",
	EmptyFlash:         "The flash is empty: a new or erased board.",
	EmptyLoopUSB:       "Without AltEsp connected it restarts over and over, and its USB port keeps disappearing and coming back.",
	EmptyLoopUART:      "Without AltEsp connected it restarts over and over, printing \"invalid header\" in the monitor.",
	DropToFlash:        "To flash it, drop a build folder on the Flash tab.",
	NoPartitionTable:   "There is a bootloader but no partition table: the bootloader has nothing to start.",
	NoApplication:      "The partition table has no application in it: the bootloader has nothing to start.",

	CopyMAC:         "Copy MAC",
	CopyAll:         "Copy all info",
	SaveReport:      "Save report…",
	ResetBoard:      "Reset board",
	EnterBootloader: "Enter bootloader",
	Copied:          "Copied",
	Saved:           "Saved to %s",
	TextFiles:       "Text files",
	AlreadyDownload: "The board is already in download mode",

	DropHint:          "Drop the firmware files or the build folder here",
	BuildFolderBtn:    "Build folder…",
	BuildFolderTip:    "Choose an ESP-IDF, PlatformIO or Arduino build folder, or one saved in parts",
	BuildFolderTitle:  "Build folder",
	AddFilesLink:      "Add files…",
	BuildFolderLink:   "Choose a build folder…",
	NoPortsHint:       "No serial ports found: plug in the board with a USB data cable",
	ColFile:           "File",
	ColAddress:        "Address",
	ColSize:           "Size",
	ColKind:           "Type",
	KindUnknown:       "Unknown",
	Kind:              func(k string) string { return k },
	Missing:           "File not found",
	Add:               "Add…",
	EraseAll:          "Erase all",
	ReadFlash:         "Read flash…",
	ReadFlashTitle:    "Read flash",
	ExportWhole:       "The whole flash, one file",
	ExportParts:       "In parts into a folder: bootloader, partition table, partitions",
	ExportTrimmed:     "One file without the erased tail",
	ExportApp:         "The application alone (for OTA)",
	ExportUserData:    "With the data of the board: NVS (Wi-Fi passwords, settings), file systems, calibration",
	ExportFolderNote:  "A folder %s is made in it, with flasher_args.json: drop it on the Flash tab to flash it back. Applications are cut to their image; erased data partitions are left out.",
	ExportTrimmedNote: "From 0x0 to the last byte that isn't FF: flashed at 0x0.",
	ExportAppNote:     "The application that boots, cut to its image: for an OTA update or its app partition.",
	ExportEncrypted:   "Flash encryption is on: the files are ciphertext and fit this chip only.",
	ExportDone:        "Saved %s to %s",
	SaveBtn:           "Save",
	OpenFolder:        "Open the folder",
	FlashBtn:          "Flash",
	Stop:              "Stop",
	Verify:            "Verify after writing",
	ResetAfter:        "Reset board after writing",
	FlashOptions:      "Ways of writing",
	OnlyChanged:       "Flash only changed files",
	NoCompress:        "No compression",
	NoStub:            "Without stub loader",
	NoStubNext:        "Takes effect on the next connection",
	NoReset:           "Don't reset after writing",
	MonitorAfter:      "Open the monitor after flashing",
	ChangeAddress:     "Change address…",
	SkipFile:          "Skip file",
	RemoveFile:        "Remove from list",
	AddressTitle:      "Address",
	AddressPrompt:     "Where to write %s (0x10000, 65536):",
	Firmware:          "Firmware images",
	AllFiles:          "All files",
	NotABuild:         "Not a build folder",
	BuildLoaded:       "%s build: %d files",
	NothingToFlash:    "No files to flash: add them or tick them in the list",
	Connecting:        "Connecting…",
	Writing:           "Writing…",
	WritingFile:       "%s (%d of %d) · %d%% · %s",
	Flashed:           "Written %[2]s in %.1[3]f s",
	PartialWrite:      "Flashing was stopped. The board may hold a partial write and may not start: flash it again.",
	WarnProblems:      "Check before writing:",
	FatalProblems:     "Writing these files would leave the board broken or fail:",
	Continue:          "Continue",
	FlashAnyway:       "Flash anyway",
	EraseAsk:          "Erase the whole flash? This cannot be undone.",
	EraseUnknown:      "Everything on the flash will be lost: the bootloader, the partition table, the applications and their data.",
	EraseBootloader:   "Lost: the bootloader and the partition table",
	ErasePartitions:   "Partitions:",
	EraseApp:          "Application:",
	ReadFirst:         "Read flash to a file first…",
	Erase:             "Erase",
	Erasing:           "Erasing the flash… it may take a minute",
	Erased:            "Flash erased in %.1f s",
	Reading:           "Reading…",
	ReadingProgress:   "Read %s of %s · %s",
	ReadDone:          "Saved %s to %s",

	BaudTip:         "Baud rate of the log: choose one or type any number",
	BadBaud:         "Invalid baud rate",
	ResetBtn:        "Reset",
	ResetTip:        "Restart the application of the board",
	MonitorBtn:      "Monitor",
	MonitorTip:      "Watch the log of the running board: it is reset into its application",
	MonitorTitle:    "Monitor · %s",
	CloseMonitorTip: "Close the monitor: the board goes on running its application",
	MonSession:      "──── %s ────",
	Pause:           "Pause",
	Resume:          "Resume",
	Clear:           "Clear",
	SaveLog:         "Save log…",
	SendHint:        "A line to send to the board",
	Send:            "Send",
	EndingTip:       "What is added to the line sent",
	NoEnding:        "None",
	MonOpening:      "Opening the port…",
	MonOpen:         "Open: %s",
	MonWaiting:      "Disconnected, waiting for the board…",
	MonClosed:       "The port is closed",
	MonJTAG:         "USB-JTAG has no log: choose the serial port of the same board to watch it",
	MonNotOpen:      "The monitor is not open",

	Busy:       "Wait for the operation to finish, or stop it",
	Stopped:    "Stopped",
	Connected:  "Connected",
	Monitoring: "Monitor",
	BaudFmt:    "%d baud",
	StubLoader: "stub loader",
	ROMLoader:  "ROM loader",

	Board:          "Board",
	NoBoard:        "No board connected",
	NoBoardHint:    "Choose the port\nand press Connect",
	RecentProjects: "Recent projects",
	NoProjects:     "The folders you flash from\nappear here",

	TabFlash:      "Flash",
	TabMemory:     "Memory",
	TabPartitions: "Partitions",

	WriteToBoard:    "Write to board",
	Changed:         "changed",
	DropChanges:     "Drop the changes",
	DropChangesAsk:  "The changes are not written to the board. Drop them?",
	NVSv1:           "NVS format v1 (ESP-IDF < v3.2)",
	NVSv2:           "NVS format v2",
	NVSUsage:        "%d entries used, %d free",
	NVSEncrypted:    "Encrypted NVS: the keys are needed to read it",
	DecryptWith:     "Decrypt with %s",
	KeyFile:         "Key file…",
	AllNamespaces:   "(all)",
	ColNamespace:    "Namespace",
	ColKey:          "Key",
	ColValue:        "Value",
	AddKey:          "Add key…",
	ChangeValue:     "Change value…",
	AddNamespace:    "Add namespace…",
	NamespacePrompt: "Name of the namespace (up to 15 characters):",
	Delete:          "Delete",
	DeleteNamespace: "Delete namespace",
	DeleteKeyAsk:    "Delete %s/%s?",
	DeleteNSAsk:     "Delete the namespace %s with its %d key(s)?",
	ExportNamespace: "Export namespace to CSV…",
	SaveNVSImage:    "Save NVS image to file…",
	NVSImported:     "Imported %d value(s)",
	NVSValueHint:    "Numbers: 42, -1, 0x2A. Blob: hex bytes (01 02 FF).",
	BadBlob:         "A blob is hex bytes: 01 02 FF",
	BadNumber:       "Not a number for %s",
	WriteNVSAsk:     "Write the NVS (%[3]d keys) to the partition %[1]s at 0x%[2]X? Its pages are written anew.",
	Up:              "Up",
	AddFiles:        "Add files…",
	AddFolder:       "Add folder…",
	NewFolder:       "New folder…",
	SaveFSImage:     "Save FS image…",
	EmptyFolder:     "The folder is empty: drop files here",
	Download:        "Download…",
	Downloaded:      "Saved %d file(s) to %s",
	Replace:         "Replace…",
	Rename:          "Rename…",
	NewName:         "Name:",
	DeleteFileAsk:   "Delete %s?",
	SPIFFSFolders:   "SPIFFS has no folders: put files with a / in their names, e.g. add a folder from the disk",
	FilesAdded:      "Added %d file(s)",
	WriteFSAsk:      "Write the changed file system to the partition %s (%s, 0x%X)? Only the changed sectors are written.",
	File:            "file",
	Folder:          "folder",

	SetELF:        "Set .elf…",
	SetELFTip:     "The .elf of the application decodes its panics",
	NoELF:         "no .elf",
	ELFShown:      ".elf: %s",
	ELFError:      "%s: %s",
	ELFMismatch:   "%s is not the .elf the application in %s was built from: the decoded panics may be wrong",
	CopyLine:      "Copy",
	DecodeAddress: "Decode address",
	NeedELF:       "Set the .elf of the application first",
	NotInELF:      "not in the .elf",
	FilterTag:     "Filter by tag",
	FilterLevel:   "Filter by level",
	FilteredTag:   "Only the tag %s",
	FilteredLevel: "Only the level %s",
	ShowAll:       "Show all",

	CoredumpAsk:      "Read the coredump from the board's coredump partition, or from a file (the partition, an ELF core file or the text printed on the UART)?",
	CoredumpNoELF:    "No .elf is set: only the addresses are shown (Monitor → Set .elf…)",
	CoredumpMismatch: "The .elf doesn't match the application that crashed",
	FromBoard:        "From the board",
	FromFile:         "From a file…",
	Exception:        "Exception",
	Crashed:          "crashed",
	Priority:         "priority",
	NoFrames:         "(no frames)",
	GenKind:          "Image",
	GenNVS:           "NVS from a CSV",
	GenLittleFS:      "LittleFS from a folder",
	GenSPIFFS:        "SPIFFS from a folder",
	GenFAT:           "FAT from a folder",
	GenMerged:        "Merged image of the flash list",
	GenSource:        "Source",
	GenVersion:       "Format",
	Browse:           "Browse…",
	GenSave:          "Save to file…",
	GenAddToList:     "Add to the flash list…",
	GenNVSInfo:       "The CSV of nvs_partition_gen: key,type,encoding,value",
	GenFSInfo:        "The size of the partition it is for; the parameters of ESP-IDF and Arduino",
	GenMergedInfo:    "%d file(s) of the Flash tab into one image flashed at 0x0",
	EditorInfo:       "Partition table at 0x%X, flash %s",
	AddRow:           "Add",
	RemoveRow:        "Remove",
	DefaultTable:     "Default table",
	ColFlags:         "Flags",
	OffsetAuto:       "auto",
	Encrypted:        "encrypted",
	TableOK:          "The table is valid",
	SaveCSV:          "Save CSV…",
	SaveBinary:       "Save binary…",
	CopyText:         "Copy",
	SaveText:         "Save…",

	OperationsLog:  "Operations log…",
	NoOperations:   "No dangerous operations yet",
	OpFailed:       "failed",
	OpEraseAll:     "Erased the whole flash",
	OpFlash:        "Flashed %s",
	OpNote:         "Note: %s",
	OpEraseSectors: "Erased %d sector(s) at 0x%X",
	OpWriteBytes:   "Wrote %d byte(s) at 0x%X",
	OpWritePart:    "Wrote %s to the partition %s",
	OpErasePart:    "Erased the partition %s",
	OpOTA:          "Made %s the OTA slot that boots",
	OpTable:        "Wrote a partition table of %d partitions",
	OpWriteNVS:     "Wrote the NVS %s (%d keys)",
	OpWriteFS:      "Wrote the file system %s (%s)",

	ShowEFuse:          "Show eFuse…",
	EFuseFilter:        "Filter: name or description",
	HideZero:           "Hide zero fields",
	WriteMode:          "Write mode",
	WriteModeBanner:    "WRITE MODE: burned eFuses can never be changed back. Double-click a field to give its new value.",
	Coding:             "Coding",
	ColDescription:     "Description",
	PendingChanges:     "To burn:",
	Review:             "Review…",
	ClearChanges:       "Clear",
	FieldProtected:     "The field is write-protected: it can't be changed",
	KeyPrompt:          "%s\n%d bytes (%d bits): type them in hex, or take a file of exactly this length.\nThe first byte is the first 8 bits of the field.",
	KeyNow:             "Now: %s",
	KeyPreview:         "BLOCK%d will read: %s",
	KeyLength:          "%s has %d bytes, the field takes %d",
	KeyBytes:           "%d bytes given, the field takes %d",
	EFusePrompt:        "%s\n%d bit(s), now %s. Bits can only be set, never cleared.",
	BadFuseValue:       "Not a number",
	FuseTooLarge:       "The field has %d bit(s)",
	ConnectFirst:       "Connect the board first",
	BurnRefused:        "The burn is refused:",
	NothingToBurn:      "Nothing to burn: the eFuses already have these values",
	BurnChanges:        "These eFuses will be burned:",
	BurnBlocks:         "Blocks: %s",
	BurnIrreversible:   "This can't be undone: the chip keeps these values forever, and a wrong one can make it unusable.",
	TypeBurn:           "Type %s to enable the Burn button:",
	Burn:               "Burn",
	Burning:            "Burning the eFuses…",
	BurnResult:         "eFuse burn",
	Burned:             "Burned and verified",
	Skipped:            "skipped",
	CodingError:        "coding error",
	Attempts:           "attempt(s)",
	ReconnectNoStub:    "Reconnect without the stub",
	ReconnectNoStubAsk: "Reconnect without the stub loader now? Then review the changes again.",
	OpBurn:             "Burned eFuses: %s",

	Settings:       "Settings…",
	SettingsTitle:  "Settings",
	SystemLanguage: "As the system",
	DefaultBaud:    "Default port baud rate",
	Shortcuts:      "Shortcuts",
	KeyConnect:     "Connect / disconnect",
	KeyFind:        "Find (Memory, Monitor)",
	FindNext:       "Find next",
	FindPrev:       "Find previous",
	SearchLog:      "Search the log",
	ResetSequence:  "Reset sequence",
	ResetAuto:      "Automatic",
	ResetBridge:    "USB-UART bridge (DTR/RTS)",
	ResetUSBJTAG:   "USB-Serial-JTAG",
	ResetManual:    "Manual (BOOT and RESET buttons)",
	ResetNext:      "Used on the next connection",
	Reconnect:      "Reconnect",
	AdapterProps:   "Adapter properties",

	PortBaud:          "Port baud rate…",
	PortBaudPrompt:    "The rate of flashing and reading:",
	PortBaudNote:      "Built-in USB ignores it. An unstable rate falls back to a lower one by itself.",
	ParamsOverride:    "Flash mode and frequency (override)…",
	AsInFile:          "as in the file",
	FlashMode:         "Mode",
	FlashFreq:         "Frequency",
	FlashSizeLabel:    "Size",
	ParamsNote:        "Written into the bootloader's header before flashing (its SHA-256 recalculated).\nA bootloader signed for Secure Boot can't be changed.",
	OverrideShown:     "override:",
	EncryptWrite:      "Encrypt while writing",
	EncryptNote:       "The chip encrypts what it writes: the result can't be verified",
	CheckUpdates:      "Check for updates…",
	CheckUpdatesTitle: "Updates",
	CheckingUpdates:   "Asking GitHub for the latest release…",
	UpdateError:       "Could not check for updates:\n\n%s",
	UpdateAvailable:   "%s %s is available (you have %s).",
	OpenReleasePage:   "Open the release page",
	UpToDate:          "You have the latest version (%s).",

	OpenImage:   "Open image…",
	BackToBoard: "Back to the board",
	EmptyFile:   "The file is empty",
	ImageOpened: "Opened %s (%s)",
	ImageShown:  "Image: %s (%s)",

	MemoryHint:      "Connect the board to see its flash, or open an image file",
	NoReadSecure:    "The board is in Secure Download Mode: its flash can't be read",
	NoReadStub:      "The flash can't be read without the stub loader, and this ROM can't read it.\nConnect again with the stub loader (the arrow next to Flash).",
	GoTo:            "Go to…",
	GoToAddr:        "Go to address",
	GoToPrompt:      "Address (0x10000, 65536):",
	PastEnd:         "Past the end of the flash (%s)",
	Find:            "Find…",
	FindMenu:        "Find bytes or text",
	FindPrompt:      "Hex bytes (E9 03 02) or text (\"esp_app\"):",
	FindEmpty:       "Type what to find",
	FindReadAll:     "Only %s of the %s flash is read. Read the whole flash to search all of it?",
	ReadWhole:       "Read the whole flash",
	FindInRead:      "Search what is read",
	FoundAt:         "Found at 0x%X",
	NotFound:        "Not found",
	SaveRegion:      "Save flash to file…",
	SaveSelection:   "Save selection to file…",
	AddressLabel:    "Address",
	SizeLabel:       "Size",
	CompareFile:     "Compare with file…",
	Comparing:       "Comparing…",
	Identical:       "The flash is the same as the file",
	DiffFound:       "Differences: %d regions, %s",
	Differences:     "Differences",
	CloseDiff:       "Close",
	CopyHex:         "Copy as hex",
	CopyC:           "Copy as C array",
	TooLargeToCopy:  "The selection is too large to copy (1 MB at most)",
	NotReadYet:      "Part of the selection is not read yet",
	WriteFileHere:   "Write file here…",
	WriteHereAsk:    "Write %s (%s) to 0x%X–0x%X? The bytes there are replaced; the rest of their sectors stays.",
	WrittenAt:       "Written %s at 0x%X",
	Write:           "Write",
	EraseSectors:    "Erase sector(s)…",
	EraseSectorsAsk: "Erase %d sector(s) from 0x%X to 0x%X? What is there will be lost.",
	ErasedAt:        "Erased %d sector(s) at 0x%X",
	EnableEditing:   "Enable editing",
	EditingHint:     "Type hex digits over the bytes, then Write changes",
	WriteChanges:    "Write changes",
	DiscardChanges:  "Discard",
	WriteEditsAsk:   "Write %d changed byte(s) (0x%X–0x%X) to the board? The sectors under them are rewritten.",
	EditsWritten:    "Written %d byte(s)",
	BackupFirst:     "Save to a file first…",
	Affected:        "Affected:",
	Bootloader:      "bootloader",
	PartitionTable:  "partition table",
	SelectedFmt:     "%s selected (0x%X–0x%X)",

	PartitionsHint:   "Connect the board to see its partitions, or open an image file",
	NoTableBoard:     "No partition table was found",
	NoTableImage:     "The image has no partition table",
	TableAt:          "Partition table at 0x%X",
	ColName:          "Name",
	ColType:          "Type",
	ColSubtype:       "Subtype",
	ColApp:           "Application",
	Boots:            "boots",
	EmptyApp:         "empty",
	Refresh:          "Refresh",
	ExportCSV:        "Export CSV…",
	ImportCSV:        "Import CSV…",
	SavePartition:    "Save partition to file…",
	WritePartition:   "Write file to partition…",
	ErasePartition:   "Erase partition…",
	MakeActive:       "Make OTA slot active",
	OpenInHex:        "Open in hex",
	NowBoots:         "%s boots next",
	EraseRest:        "Erase the rest of the partition",
	WritePartAsk:     "Write %s (%s) to the partition %s at 0x%X?",
	TooLargeForPart:  "%s is %s, the partition %s is %s",
	PartWritten:      "Written to %s",
	ErasePartAsk:     "Erase the partition %s (0x%X, %s)? What is in it will be lost.",
	PartErased:       "Erased %s",
	TableProblems:    "The table can't be written:",
	WriteTableAsk:    "Write this partition table at 0x%X? A wrong table makes the board unbootable, and the data of moved partitions is lost.",
	WriteTable:       "Write the table",
	TableWritten:     "Partition table written",
	AppProject:       "Project",
	AppVersion:       "Version",
	AppBuilt:         "Built",
	AppSecureVersion: "Secure version",
	AppELF:           "ELF SHA-256",
	AppBoots:         "The bootloader starts this application.",

	ToolEfuse:           "eFuse",
	ToolPartitionEditor: "Partition table editor",
	ToolImages:          "Image generator",
	ToolCoredump:        "Coredump analysis",

	NotConnected:   "Not connected",
	NoPortSelected: "No port selected",

	Theme:       "Theme",
	ThemeDark:   "Dark",
	ThemeLight:  "Light",
	Language:    "Language",
	About:       "About",
	AboutText:   "Flash and explore ESP32 boards: simple outside, powerful inside.",
	LicenseText: "MIT License",
	Trademark:   "ESP32 is a trademark of Espressif Systems; AltEsp is not affiliated with or endorsed by Espressif.",
	StubCredit:  "The flasher stub is esp-flasher-stub by Espressif Systems, MIT License.",

	InstallLink:   "Install",
	UpdateLink:    "Update",
	UninstallLink: "Uninstall",
	Installed:     "AltEsp is installed",
	InstallAsk:    "AltEsp will be copied to %s and added to the Start menu, the desktop and the list of installed apps. If it is already installed there, it is updated. The settings and the projects stay as they are.\n\nAltEsp will then restart from there.",
	InstallFailed: "AltEsp could not be installed:\n\n%s",
	UninstallAsk:  "Remove AltEsp from this computer?\n\nThe settings and the projects stay in %s",
	Uninstalled:   "AltEsp has been removed.",
}

var russian = Strings{
	RefreshPortsTip: "Найти порты заново: отключённая плата уходит из списка",
	PortsFound:      "Найдено портов: %d",
	SectorErased:    "пусто (FF)",
	SectorData:      "данные",
	SectorChecking:  "проверяется",
	BoardUnplugged:  "Плата отключена",
	PortTip:         "Порт платы",
	NoPorts:         "Нет портов",
	Connect:         "Подключить",
	ConnectTip:      "Перевести плату в режим загрузки и прочитать чип",
	Tools:           "Инструменты",
	MenuTip:         "Меню",
	Disconnect:      "Отключить",
	Cancel:          "Отмена",
	ManualHint:      "Переведите плату в режим загрузки: удерживая BOOT, нажмите и отпустите RESET, затем отпустите BOOT",

	FlashLabel: "Flash",
	Crystal:    "Кварц",
	OnBoard:    "На плате:",

	NoFirmware:         "Прошивки нет",
	IncompleteFirmware: "Прошивка неполная",
	EmptyFlash:         "Flash пустая: новая или стёртая плата.",
	EmptyLoopUSB:       "Без подключения к AltEsp она перезагружается по кругу, а её USB-порт то пропадает, то появляется.",
	EmptyLoopUART:      "Без подключения к AltEsp она перезагружается по кругу, а в мониторе повторяется «invalid header».",
	DropToFlash:        "Чтобы прошить, перетащите папку сборки на вкладку «Прошивка».",
	NoPartitionTable:   "Загрузчик есть, а таблицы разделов нет: загрузчику нечего запускать.",
	NoApplication:      "В таблице разделов нет приложения: загрузчику нечего запускать.",

	CopyMAC:         "Копировать MAC",
	CopyAll:         "Копировать всё",
	SaveReport:      "Сохранить отчёт…",
	ResetBoard:      "Перезагрузить плату",
	EnterBootloader: "Войти в загрузчик",
	Copied:          "Скопировано",
	Saved:           "Сохранено в %s",
	TextFiles:       "Текстовые файлы",
	AlreadyDownload: "Плата уже в режиме загрузки",

	DropHint:         "Перетащите сюда файлы прошивки или папку сборки",
	BuildFolderBtn:   "Папка сборки…",
	BuildFolderTip:   "Выбрать папку сборки ESP-IDF, PlatformIO или Arduino, или сохранённую по частям",
	BuildFolderTitle: "Папка сборки",
	AddFilesLink:     "Добавить файлы…",
	BuildFolderLink:  "Выбрать папку сборки…",
	NoPortsHint:      "Последовательные порты не найдены: подключите плату USB-кабелем с данными",
	ColFile:          "Файл",
	ColAddress:       "Адрес",
	ColSize:          "Размер",
	ColKind:          "Тип",
	KindUnknown:      "Неизвестно",
	Kind: func(k string) string {
		if t, ok := map[string]string{
			"Bootloader": "Загрузчик", "Application": "Приложение", "Partition table": "Таблица разделов",
			"Merged image": "Объединённый образ", "OTA data": "Данные OTA", "PHY calibration": "Калибровка PHY",
			"Data": "Данные", "Unknown": "Неизвестно",
		}[k]; ok {
			return t
		}
		return k
	},
	Missing:           "Файл не найден",
	Add:               "Добавить…",
	EraseAll:          "Стереть всё",
	ReadFlash:         "Прочитать flash…",
	ReadFlashTitle:    "Прочитать flash",
	ExportWhole:       "Вся flash одним файлом",
	ExportParts:       "По частям в папку: загрузчик, таблица разделов, разделы",
	ExportTrimmed:     "Одним файлом без пустого хвоста",
	ExportApp:         "Только приложение (для OTA)",
	ExportUserData:    "С данными платы: NVS (пароли Wi-Fi, настройки), файловые системы, калибровка",
	ExportFolderNote:  "Внутри будет создана папка %s с flasher_args.json: перетащите её на вкладку «Прошивка», чтобы прошить обратно. Приложения обрезаются по размеру образа; пустые разделы данных не сохраняются.",
	ExportTrimmedNote: "С 0x0 до последнего байта, отличного от FF: прошивается с адреса 0x0.",
	ExportAppNote:     "Загружаемое приложение, обрезанное по размеру образа: для OTA-обновления или записи в раздел приложения.",
	ExportEncrypted:   "Включено шифрование flash: файлы зашифрованы и подходят только этому чипу.",
	ExportDone:        "%s сохранено в %s",
	SaveBtn:           "Сохранить",
	OpenFolder:        "Открыть папку",
	FlashBtn:          "Прошить",
	Stop:              "Стоп",
	Verify:            "Проверить после записи",
	ResetAfter:        "Перезагрузить плату после записи",
	FlashOptions:      "Режимы записи",
	OnlyChanged:       "Прошивать только изменённые файлы",
	NoCompress:        "Без сжатия",
	NoStub:            "Без загрузчика-заглушки",
	NoStubNext:        "Применится при следующем подключении",
	NoReset:           "Не перезагружать после записи",
	MonitorAfter:      "Открывать монитор после прошивки",
	ChangeAddress:     "Изменить адрес…",
	SkipFile:          "Пропустить файл",
	RemoveFile:        "Убрать из списка",
	AddressTitle:      "Адрес",
	AddressPrompt:     "Куда записать %s (0x10000, 65536):",
	Firmware:          "Образы прошивки",
	AllFiles:          "Все файлы",
	NotABuild:         "Это не папка сборки",
	BuildLoaded:       "Сборка %s: файлов — %d",
	NothingToFlash:    "Нет файлов для прошивки: добавьте их или отметьте в списке",
	Connecting:        "Подключение…",
	Writing:           "Запись…",
	WritingFile:       "%s (%d из %d) · %d%% · %s",
	Flashed:           "Записано %[2]s за %.1[3]f с",
	PartialWrite:      "Прошивка остановлена. На плате может остаться недописанный образ, и она может не запуститься: прошейте её заново.",
	WarnProblems:      "Проверьте перед записью:",
	FatalProblems:     "Запись этих файлов сломает плату или не удастся:",
	Continue:          "Продолжить",
	FlashAnyway:       "Всё равно прошить",
	EraseAsk:          "Стереть всю flash? Это нельзя отменить.",
	EraseUnknown:      "Всё содержимое flash будет потеряно: загрузчик, таблица разделов, приложения и их данные.",
	EraseBootloader:   "Будут потеряны: загрузчик и таблица разделов",
	ErasePartitions:   "Разделы:",
	EraseApp:          "Приложение:",
	ReadFirst:         "Сначала прочитать flash в файл…",
	Erase:             "Стереть",
	Erasing:           "Стирание flash… это может занять минуту",
	Erased:            "Flash стёрта за %.1f с",
	Reading:           "Чтение…",
	ReadingProgress:   "Прочитано %s из %s · %s",
	ReadDone:          "%s сохранено в %s",

	BaudTip:         "Скорость лога: выберите или введите любое число",
	BadBaud:         "Неверная скорость",
	ResetBtn:        "Сброс",
	ResetTip:        "Перезапустить приложение платы",
	MonitorBtn:      "Монитор",
	MonitorTip:      "Смотреть лог работающей платы: она сбрасывается в своё приложение",
	MonitorTitle:    "Монитор · %s",
	CloseMonitorTip: "Закрыть монитор: плата продолжает работать в своём приложении",
	MonSession:      "──── %s ────",
	Pause:           "Пауза",
	Resume:          "Продолжить",
	Clear:           "Очистить",
	SaveLog:         "Сохранить лог…",
	SendHint:        "Строка для отправки на плату",
	Send:            "Отправить",
	EndingTip:       "Что добавляется к отправляемой строке",
	NoEnding:        "Ничего",
	MonOpening:      "Открытие порта…",
	MonOpen:         "Открыт: %s",
	MonWaiting:      "Отключено, ожидание платы…",
	MonClosed:       "Порт закрыт",
	MonJTAG:         "У USB-JTAG нет лога: выберите последовательный порт той же платы",
	MonNotOpen:      "Монитор не открыт",

	Busy:       "Дождитесь окончания операции или остановите её",
	Stopped:    "Остановлено",
	Connected:  "Подключено",
	Monitoring: "Монитор",
	BaudFmt:    "%d бод",
	StubLoader: "загрузчик-заглушка",
	ROMLoader:  "загрузчик ROM",

	Board:          "Плата",
	NoBoard:        "Плата не подключена",
	NoBoardHint:    "Выберите порт\nи нажмите «Подключить»",
	RecentProjects: "Недавние проекты",
	NoProjects:     "Здесь появятся папки,\nиз которых вы прошиваете",

	TabFlash:      "Прошивка",
	TabMemory:     "Память",
	TabPartitions: "Разделы",

	WriteToBoard:    "Записать на плату",
	Changed:         "изменено",
	DropChanges:     "Отбросить изменения",
	DropChangesAsk:  "Изменения не записаны на плату. Отбросить их?",
	NVSv1:           "формат NVS v1 (ESP-IDF < v3.2)",
	NVSv2:           "формат NVS v2",
	NVSUsage:        "занято записей: %d, свободно: %d",
	NVSEncrypted:    "Зашифрованный NVS: для чтения нужны ключи",
	DecryptWith:     "Расшифровать ключами %s",
	KeyFile:         "Файл ключей…",
	AllNamespaces:   "(все)",
	ColNamespace:    "Пространство",
	ColKey:          "Ключ",
	ColValue:        "Значение",
	AddKey:          "Добавить ключ…",
	ChangeValue:     "Изменить значение…",
	AddNamespace:    "Добавить пространство…",
	NamespacePrompt: "Имя пространства имён (до 15 символов):",
	Delete:          "Удалить",
	DeleteNamespace: "Удалить пространство",
	DeleteKeyAsk:    "Удалить %s/%s?",
	DeleteNSAsk:     "Удалить пространство %s вместе с ключами (%d)?",
	ExportNamespace: "Экспорт пространства в CSV…",
	SaveNVSImage:    "Сохранить образ NVS в файл…",
	NVSImported:     "Импортировано значений: %d",
	NVSValueHint:    "Числа: 42, -1, 0x2A. Blob: байты в hex (01 02 FF).",
	BadBlob:         "Blob — это байты в hex: 01 02 FF",
	BadNumber:       "Не число для %s",
	WriteNVSAsk:     "Записать NVS (ключей: %[3]d) в раздел %[1]s по адресу 0x%[2]X? Его страницы будут записаны заново.",
	Up:              "Вверх",
	AddFiles:        "Добавить файлы…",
	AddFolder:       "Добавить папку…",
	NewFolder:       "Новая папка…",
	SaveFSImage:     "Сохранить образ ФС…",
	EmptyFolder:     "Папка пуста: перетащите сюда файлы",
	Download:        "Скачать…",
	Downloaded:      "Сохранено файлов: %d в %s",
	Replace:         "Заменить…",
	Rename:          "Переименовать…",
	NewName:         "Имя:",
	DeleteFileAsk:   "Удалить %s?",
	SPIFFSFolders:   "В SPIFFS нет папок: папкой служит / в имени файла, например добавьте папку с диска",
	FilesAdded:      "Добавлено файлов: %d",
	WriteFSAsk:      "Записать изменённую файловую систему в раздел %s (%s, 0x%X)? Записываются только изменённые секторы.",
	File:            "файл",
	Folder:          "папка",

	SetELF:        "Указать .elf…",
	SetELFTip:     ".elf приложения расшифровывает его паники",
	NoELF:         "нет .elf",
	ELFShown:      ".elf: %s",
	ELFError:      "%s: %s",
	ELFMismatch:   "%s — не тот .elf, из которого собрано приложение в %s: расшифровка паник может быть неверной",
	CopyLine:      "Копировать",
	DecodeAddress: "Расшифровать адрес",
	NeedELF:       "Сначала укажите .elf приложения",
	NotInELF:      "нет в .elf",
	FilterTag:     "Фильтр по тегу",
	FilterLevel:   "Фильтр по уровню",
	FilteredTag:   "Только тег %s",
	FilteredLevel: "Только уровень %s",
	ShowAll:       "Показать всё",

	CoredumpAsk:      "Прочитать coredump из раздела coredump платы или из файла (раздел, ELF core или текст, выведенный в UART)?",
	CoredumpNoELF:    ".elf не указан: будут показаны только адреса (Монитор → Указать .elf…)",
	CoredumpMismatch: ".elf не соответствует упавшему приложению",
	FromBoard:        "С платы",
	FromFile:         "Из файла…",
	Exception:        "Исключение",
	Crashed:          "упала",
	Priority:         "приоритет",
	NoFrames:         "(нет кадров)",
	GenKind:          "Образ",
	GenNVS:           "NVS из CSV",
	GenLittleFS:      "LittleFS из папки",
	GenSPIFFS:        "SPIFFS из папки",
	GenFAT:           "FAT из папки",
	GenMerged:        "Объединённый образ из списка прошивки",
	GenSource:        "Источник",
	GenVersion:       "Формат",
	Browse:           "Обзор…",
	GenSave:          "Сохранить в файл…",
	GenAddToList:     "Добавить в список прошивки…",
	GenNVSInfo:       "CSV для nvs_partition_gen: key,type,encoding,value",
	GenFSInfo:        "Размер раздела, для которого образ; параметры ESP-IDF и Arduino",
	GenMergedInfo:    "Файлов вкладки «Прошивка»: %d — в один образ для записи с 0x0",
	EditorInfo:       "Таблица разделов по адресу 0x%X, flash %s",
	AddRow:           "Добавить",
	RemoveRow:        "Удалить",
	DefaultTable:     "Таблица по умолчанию",
	ColFlags:         "Флаги",
	OffsetAuto:       "авто",
	Encrypted:        "шифрованный",
	TableOK:          "Таблица верна",
	SaveCSV:          "Сохранить CSV…",
	SaveBinary:       "Сохранить двоичный…",
	CopyText:         "Копировать",
	SaveText:         "Сохранить…",

	OperationsLog:  "Журнал операций…",
	NoOperations:   "Опасных операций ещё не было",
	OpFailed:       "ошибка",
	OpEraseAll:     "Стёрта вся flash",
	OpFlash:        "Прошито: %s",
	OpNote:         "Замечание: %s",
	OpEraseSectors: "Стёрто секторов: %d с 0x%X",
	OpWriteBytes:   "Записано байтов: %d по адресу 0x%X",
	OpWritePart:    "Записан %s в раздел %s",
	OpErasePart:    "Стёрт раздел %s",
	OpOTA:          "OTA-слот %s сделан загружаемым",
	OpTable:        "Записана таблица из %d разделов",
	OpWriteNVS:     "Записан NVS %s (ключей: %d)",
	OpWriteFS:      "Записана файловая система %s (%s)",

	ShowEFuse:          "Показать eFuse…",
	EFuseFilter:        "Фильтр: имя или описание",
	HideZero:           "Скрыть нулевые поля",
	WriteMode:          "Режим записи",
	WriteModeBanner:    "РЕЖИМ ЗАПИСИ: прожжённые eFuse нельзя вернуть назад. Дважды щёлкните поле, чтобы задать новое значение.",
	Coding:             "Кодирование",
	ColDescription:     "Описание",
	PendingChanges:     "К прожигу:",
	Review:             "Проверить…",
	ClearChanges:       "Очистить",
	FieldProtected:     "Поле защищено от записи: его нельзя изменить",
	KeyPrompt:          "%s\nБайт: %d (бит: %d). Введите их в hex или возьмите файл ровно этой длины.\nПервый байт — первые 8 бит поля.",
	KeyNow:             "Сейчас: %s",
	KeyPreview:         "BLOCK%d будет читаться: %s",
	KeyLength:          "В %s байт: %d, а поле занимает %d",
	KeyBytes:           "Задано байт: %d, а поле занимает %d",
	EFusePrompt:        "%s\nБит: %d, сейчас %s. Биты можно только установить, но не сбросить.",
	BadFuseValue:       "Не число",
	FuseTooLarge:       "В поле бит: %d",
	ConnectFirst:       "Сначала подключите плату",
	BurnRefused:        "Прожиг отклонён:",
	NothingToBurn:      "Прожигать нечего: в eFuse уже эти значения",
	BurnChanges:        "Будут прожжены eFuse:",
	BurnBlocks:         "Блоки: %s",
	BurnIrreversible:   "Это необратимо: чип навсегда сохранит эти значения, а ошибочное может сделать его непригодным.",
	TypeBurn:           "Введите %s, чтобы включить кнопку «Прожечь»:",
	Burn:               "Прожечь",
	Burning:            "Прожиг eFuse…",
	BurnResult:         "Прожиг eFuse",
	Burned:             "Прожжено и проверено",
	Skipped:            "пропущено",
	CodingError:        "ошибка кодирования",
	Attempts:           "попыток",
	ReconnectNoStub:    "Переподключиться без заглушки",
	ReconnectNoStubAsk: "Переподключиться без загрузчика-заглушки сейчас? Затем проверьте изменения снова.",
	OpBurn:             "Прожжены eFuse: %s",

	Settings:       "Настройки…",
	SettingsTitle:  "Настройки",
	SystemLanguage: "Как в системе",
	DefaultBaud:    "Скорость порта по умолчанию",
	Shortcuts:      "Горячие клавиши",
	KeyConnect:     "Подключить / отключить",
	KeyFind:        "Найти (Память, Монитор)",
	FindNext:       "Найти далее",
	FindPrev:       "Найти назад",
	SearchLog:      "Поиск в логе",
	ResetSequence:  "Последовательность сброса",
	ResetAuto:      "Автоматически",
	ResetBridge:    "Мост USB-UART (DTR/RTS)",
	ResetUSBJTAG:   "USB-Serial-JTAG",
	ResetManual:    "Вручную (кнопки BOOT и RESET)",
	ResetNext:      "Применится при следующем подключении",
	Reconnect:      "Переподключить",
	AdapterProps:   "Свойства адаптера",

	PortBaud:          "Скорость порта…",
	PortBaudPrompt:    "Скорость прошивки и чтения:",
	PortBaudNote:      "Встроенный USB её не использует. При нестабильной скорости программа сама перейдёт на меньшую.",
	ParamsOverride:    "Режим и частота flash (переопределить)…",
	AsInFile:          "как в файле",
	FlashMode:         "Режим",
	FlashFreq:         "Частота",
	FlashSizeLabel:    "Размер",
	ParamsNote:        "Записывается в заголовок загрузчика перед прошивкой (SHA-256 пересчитывается).\nЗагрузчик, подписанный для Secure Boot, изменить нельзя.",
	OverrideShown:     "переопределено:",
	EncryptWrite:      "Шифровать при записи",
	EncryptNote:       "Чип шифрует то, что записывает: результат нельзя проверить",
	CheckUpdates:      "Проверить обновления…",
	CheckUpdatesTitle: "Обновления",
	CheckingUpdates:   "Запрос последней версии на GitHub…",
	UpdateError:       "Не удалось проверить обновления:\n\n%s",
	UpdateAvailable:   "Доступна версия %s %s (у вас %s).",
	OpenReleasePage:   "Открыть страницу версии",
	UpToDate:          "У вас последняя версия (%s).",

	OpenImage:   "Открыть образ…",
	BackToBoard: "Вернуться к плате",
	EmptyFile:   "Файл пустой",
	ImageOpened: "Открыт %s (%s)",
	ImageShown:  "Образ: %s (%s)",

	MemoryHint:      "Подключите плату, чтобы увидеть её flash, или откройте файл образа",
	NoReadSecure:    "Плата в режиме Secure Download: её flash нельзя прочитать",
	NoReadStub:      "Без загрузчика-заглушки flash не прочитать, а этот ROM читать не умеет.\nПодключитесь снова с загрузчиком-заглушкой (стрелка у кнопки «Прошить»).",
	GoTo:            "Перейти…",
	GoToAddr:        "Перейти к адресу",
	GoToPrompt:      "Адрес (0x10000, 65536):",
	PastEnd:         "За концом flash (%s)",
	Find:            "Найти…",
	FindMenu:        "Найти байты или текст",
	FindPrompt:      "Байты в hex (E9 03 02) или текст (\"esp_app\"):",
	FindEmpty:       "Введите, что искать",
	FindReadAll:     "Прочитано только %s из %s flash. Прочитать всю flash, чтобы искать по всей?",
	ReadWhole:       "Прочитать всю flash",
	FindInRead:      "Искать в прочитанном",
	FoundAt:         "Найдено по адресу 0x%X",
	NotFound:        "Не найдено",
	SaveRegion:      "Сохранить flash в файл…",
	SaveSelection:   "Сохранить выделенное в файл…",
	AddressLabel:    "Адрес",
	SizeLabel:       "Размер",
	CompareFile:     "Сравнить с файлом…",
	Comparing:       "Сравнение…",
	Identical:       "Flash совпадает с файлом",
	DiffFound:       "Различия: областей — %d, %s",
	Differences:     "Различия",
	CloseDiff:       "Закрыть",
	CopyHex:         "Копировать как hex",
	CopyC:           "Копировать как массив C",
	TooLargeToCopy:  "Выделение слишком велико для копирования (не больше 1 МБ)",
	NotReadYet:      "Часть выделения ещё не прочитана",
	WriteFileHere:   "Записать файл сюда…",
	WriteHereAsk:    "Записать %s (%s) в 0x%X–0x%X? Байты там будут заменены, остальное в их секторах сохранится.",
	WrittenAt:       "Записано %s по адресу 0x%X",
	Write:           "Записать",
	EraseSectors:    "Стереть сектор(ы)…",
	EraseSectorsAsk: "Стереть секторов — %d, с 0x%X по 0x%X? Их содержимое будет потеряно.",
	ErasedAt:        "Стёрто секторов — %d, с 0x%X",
	EnableEditing:   "Разрешить правку",
	EditingHint:     "Вводите hex-цифры поверх байтов, затем «Записать изменения»",
	WriteChanges:    "Записать изменения",
	DiscardChanges:  "Отменить",
	WriteEditsAsk:   "Записать изменённых байтов — %d (0x%X–0x%X) на плату? Секторы под ними будут перезаписаны.",
	EditsWritten:    "Записано байтов — %d",
	BackupFirst:     "Сначала сохранить в файл…",
	Affected:        "Затронуто:",
	Bootloader:      "загрузчик",
	PartitionTable:  "таблица разделов",
	SelectedFmt:     "выделено %s (0x%X–0x%X)",

	PartitionsHint:   "Подключите плату, чтобы увидеть её разделы, или откройте файл образа",
	NoTableBoard:     "Таблица разделов не найдена",
	NoTableImage:     "В образе нет таблицы разделов",
	TableAt:          "Таблица разделов по адресу 0x%X",
	ColName:          "Имя",
	ColType:          "Тип",
	ColSubtype:       "Подтип",
	ColApp:           "Приложение",
	Boots:            "загружается",
	EmptyApp:         "пусто",
	Refresh:          "Обновить",
	ExportCSV:        "Экспорт CSV…",
	ImportCSV:        "Импорт CSV…",
	SavePartition:    "Сохранить раздел в файл…",
	WritePartition:   "Записать файл в раздел…",
	ErasePartition:   "Стереть раздел…",
	MakeActive:       "Сделать OTA-слот активным",
	OpenInHex:        "Открыть в hex",
	NowBoots:         "Следующим загрузится %s",
	EraseRest:        "Стереть остаток раздела",
	WritePartAsk:     "Записать %s (%s) в раздел %s по адресу 0x%X?",
	TooLargeForPart:  "%s занимает %s, а раздел %s — %s",
	PartWritten:      "Записано в %s",
	ErasePartAsk:     "Стереть раздел %s (0x%X, %s)? Его содержимое будет потеряно.",
	PartErased:       "Раздел %s стёрт",
	TableProblems:    "Таблицу нельзя записать:",
	WriteTableAsk:    "Записать эту таблицу разделов по адресу 0x%X? Ошибочная таблица сделает плату незагружаемой, а данные перемещённых разделов будут потеряны.",
	WriteTable:       "Записать таблицу",
	TableWritten:     "Таблица разделов записана",
	AppProject:       "Проект",
	AppVersion:       "Версия",
	AppBuilt:         "Собрано",
	AppSecureVersion: "Secure version",
	AppELF:           "SHA-256 ELF",
	AppBoots:         "Загрузчик запускает это приложение.",

	ToolEfuse:           "eFuse",
	ToolPartitionEditor: "Редактор таблицы разделов",
	ToolImages:          "Генератор образов",
	ToolCoredump:        "Анализ coredump",

	NotConnected:   "Не подключено",
	NoPortSelected: "Порт не выбран",

	Theme:       "Тема",
	ThemeDark:   "Тёмная",
	ThemeLight:  "Светлая",
	Language:    "Язык",
	About:       "О программе",
	AboutText:   "Прошивка и исследование плат ESP32: простая снаружи, мощная внутри.",
	LicenseText: "Лицензия MIT",
	Trademark:   "ESP32 is a trademark of Espressif Systems; AltEsp is not affiliated with or endorsed by Espressif.",
	StubCredit:  "Загрузчик-заглушка — esp-flasher-stub от Espressif Systems, лицензия MIT.",

	InstallLink:   "Установить",
	UpdateLink:    "Обновить",
	UninstallLink: "Удалить",
	Installed:     "AltEsp установлен",
	InstallAsk:    "AltEsp будет скопирован в %s и добавлен в меню «Пуск», на рабочий стол и в список установленных приложений. Если он там уже установлен, он обновится. Настройки и проекты останутся как есть.\n\nЗатем AltEsp перезапустится оттуда.",
	InstallFailed: "Не удалось установить AltEsp:\n\n%s",
	UninstallAsk:  "Удалить AltEsp с этого компьютера?\n\nНастройки и проекты останутся в %s",
	Uninstalled:   "AltEsp удалён.",
}

var catalog = i18n.NewCatalog(english, map[string]Strings{"ru": russian})

// T returns the texts in the language of the interface
func T() *Strings { return catalog.Get(ui.Language()) }

// Languages are the languages of the interface
var Languages = []string{"en", "ru"}

// LanguageName returns the name of a language in that language
func LanguageName(lang string) string {
	switch lang {
	case "ru":
		return "Русский"
	}
	return "English"
}

// SetLanguage switches the interface to the language; "" is the system's
func SetLanguage(lang string) {
	if lang != "" {
		ui.SetLanguage(lang)
	}
}
