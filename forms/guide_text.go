package forms

import (
	"github.com/ipoluianov/nui/ui"
	"github.com/ipoluianov/nui/ui/i18n"
)

// Guide is the text of the ESP32 guide in one language: the topics, and
// the words of their pictures
type Guide struct {
	Title, Menu, Prev, Next string

	ChipTitle, ChipText                         string
	ChipBox, CPU, Radio, Cache, Peripherals     string
	FlashChip, PSRAM                            string
	MemoryTitle, MemoryText                     string
	PowerOn, PowerOff, RAMBox, RAMNote          string
	FlashBox, FlashNote                         string
	BootTitle, BootText                         string
	ROMStep, InChip, BootloaderStep, TableStep  string
	AppStep, DownloadStep, BootPin              string
	MapTitle, MapText                           string
	AreaText                                    []string
	NVSTitle, NVSText, Page, NVSWrite, NVSMove  string
	OTATitle, OTAText, Runs, Downloading        string
	OTADownload, OTASwitch, OTARestart          string
	OTARollback                                 string
	FSTitle, FSText                             string
	LittleFSNote, SPIFFSNote, FATNote           string
	EFuseTitle, EFuseText, EFuseOnce            string
	SecurityTitle, SecurityText, EncryptionBox  string
	InChipPlain, KeyInEFuse, InFlashEnc         string
	SignedImage, ChangedImage, BootloaderChecks string
	Starts, Refused                             string
	USBTitle, USBText, ViaBridge, ViaOwnUSB     string
	Computer, USBInside, PortGone, Resetting    string
	FamilyTitle, FamilyText                     string
	ColChip, ColCore, ColRadio                  string
	PanicTitle, PanicText, LogBox, SameBuild    string
	Decoded                                     string
}

var guideEnglish = Guide{
	Title: "How the ESP32 works",
	Menu:  "How the ESP32 works…",
	Prev:  "← Back",
	Next:  "Next →",

	ChipTitle: "Inside the chip",
	ChipText: "The ESP32 is a family of Espressif microcontrollers with Wi-Fi and Bluetooth. Inside the chip: one or two processor cores; " +
		"ROM with the factory code that never changes; SRAM for the running program and its data; the radio; the peripherals that talk to the world " +
		"(pins, UART, SPI, I²C, ADC, USB); and eFuses, bits written once. The program itself is kept in a separate SPI flash chip, often in the same " +
		"package or module. The cores run code straight from it through the cache, the moving dots. Some boards add PSRAM, extra RAM on the same bus.",
	ChipBox:     "ESP32 chip",
	CPU:         "CPU\n1–2 cores",
	Radio:       "Radio: Wi-Fi · Bluetooth",
	Cache:       "Cache",
	Peripherals: "Peripherals\nGPIO · UART · SPI · I²C · ADC · USB",
	FlashChip:   "SPI flash\nprogram and data",
	PSRAM:       "PSRAM\n(on some boards)",

	MemoryTitle: "Flash and RAM",
	MemoryText: "There are two kinds of memory. SRAM is fast but forgets everything when the power goes: it holds what the program is doing now. " +
		"It is a few hundred kilobytes. The flash keeps its content without power: usually 2 to 16 MB, with the program and the data that must survive a restart. " +
		"The flash is not overwritten in place: a 4 KB sector is erased first (all bytes become FF), then written. Each sector survives " +
		"about a hundred thousand erases, so the firmware stores data in new places instead of rewriting the same ones.",
	PowerOn:   "Power on",
	PowerOff:  "Power off",
	RAMBox:    "SRAM",
	RAMNote:   "fast, a few hundred KB, lost without power",
	FlashBox:  "Flash",
	FlashNote: "slower, megabytes, kept without power",

	BootTitle: "How the board starts",
	BootText: "At power-on the chip runs the code in its ROM. If the BOOT pin is held low at that moment (GPIO0 on the ESP32 and the S-series, " +
		"GPIO9 on most C- and H-series), the ROM goes into download mode and waits for commands over UART or USB: this is how AltEsp and the other " +
		"tools flash the chip. Otherwise the ROM loads the second-stage bootloader from the flash (at 0x1000 on the ESP32 and S2, 0x2000 on the C5 and P4, " +
		"0x0 on the others). The bootloader reads the partition table, usually at 0x8000, picks the application and starts it.",
	ROMStep:        "ROM",
	InChip:         "inside the chip",
	BootloaderStep: "Bootloader",
	TableStep:      "Partition\ntable",
	AppStep:        "Application",
	DownloadStep:   "Download mode:\nwaits for a program",
	BootPin:        "BOOT held at reset: the ROM waits for commands instead of starting the bootloader",

	MapTitle: "The flash map and partitions",
	MapText: "The flash is divided into areas. At its start are the bootloader and the partition table; the table lists the other areas, the partitions: " +
		"their name, type (app or data), subtype, address and size. Applications live in app partitions, the data in data partitions. " +
		"In AltEsp the Partitions tab shows the table of the board, the Memory tab its bytes.",
	AreaText: []string{
		"The second-stage bootloader: started by the ROM, it reads the partition table and starts the application.",
		"The partition table: where each partition is and what it holds. The bootloader and the application read it.",
		"NVS: the settings of the firmware, as keys and values — the Wi-Fi network, counters, calibration.",
		"otadata: which OTA slot boots. It is small, two sectors, so that one survives while the other is written.",
		"phy_init: the radio calibration data.",
		"ota_0: an application slot. The application runs from here.",
		"ota_1: the second slot, where an update is written while ota_0 runs.",
		"storage: a file system for the files of the firmware — web pages, configs, logs.",
	},

	NVSTitle: "NVS: firmware settings",
	NVSText: "NVS (non-volatile storage) keeps the small settings of the firmware: the Wi-Fi network and its password, counters, calibration. " +
		"They are pairs of a key and a value, grouped by namespace (namespace wifi, key ssid). As the flash can't be overwritten in place, a new value " +
		"is written to the next free entry and the old one is marked erased. When a page is full, the values still in use move to a fresh page " +
		"and the old page is erased: the wear is spread over the sectors. In AltEsp: Partitions, right-click nvs, to see and edit them.",
	Page:     "Page",
	NVSWrite: "Each write goes to the next free entry; the old value of the key is marked erased",
	NVSMove:  "The page is full: the values in use move to a fresh page, the old page is erased",

	OTATitle: "OTA: updates without a cable",
	OTAText: "OTA (over the air) updates the firmware over the network. There are two application slots, ota_0 and ota_1. While one runs, the new " +
		"version is downloaded into the other. Then otadata, a small partition, is changed to point to it, and the chip restarts into the new version. " +
		"With rollback on, a new version that doesn't confirm it works is replaced by the old one at the next start. " +
		"In AltEsp the Partitions tab marks the slot that boots; right-click to switch it.",
	Runs:        "runs",
	Downloading: "downloading…",
	OTADownload: "ota_0 runs; the new version is downloaded into ota_1",
	OTASwitch:   "otadata now points to ota_1",
	OTARestart:  "After a restart the bootloader starts ota_1",
	OTARollback: "If v1.1 fails before confirming, the bootloader goes back to ota_0",

	FSTitle: "File systems",
	FSText: "For files — web pages, configs, logs — the firmware keeps a file system in a data partition. AltEsp opens all three kinds: " +
		"Partitions, right-click the partition, to see the files, take them off, add or delete them and write the partition back.",
	LittleFSNote: "LittleFS: survives a power cut in the middle of a write; folders; the usual choice today.",
	SPIFFSNote:   "SPIFFS: the older one; no real folders, names are paths; slower as it fills.",
	FATNote:      "FAT: the file system of memory cards, on top of a wear-levelling layer that spreads the writes.",

	EFuseTitle: "eFuses",
	EFuseText: "eFuses are bits inside the chip written once. A bit starts as 0 and can be burned to 1, never back. They hold the MAC address, " +
		"the calibration and the chip revision, and the security settings: the keys of flash encryption and secure boot, the switches that turn off " +
		"JTAG or download mode. A wrong burn can leave a chip unusable for good, so AltEsp burns only in a separate mode, after you type BURN.",
	EFuseOnce: "A bit is burned from 0 to 1 once, and never goes back",

	SecurityTitle: "Encryption and Secure Boot",
	SecurityText: "Flash encryption: the chip encrypts the flash with a key kept in its eFuses. A copy of the flash can't be read without that chip, " +
		"while the chip decrypts it as it runs. Secure Boot: the bootloader and the application are signed, and the chip runs only what is signed " +
		"with your key, so a changed firmware won't start. Both are turned on by burning eFuses and mostly can't be turned off.",
	EncryptionBox:    "Flash encryption",
	InChipPlain:      "In the chip, as it runs:",
	KeyInEFuse:       "key in eFuse",
	InFlashEnc:       "In the flash:",
	SignedImage:      "image signed with your key",
	ChangedImage:     "image changed or unsigned",
	BootloaderChecks: "the signature is checked",
	Starts:           "starts",
	Refused:          "doesn't start",

	USBTitle: "Connecting to a computer",
	USBText: "There are two ways. Through a USB-to-UART bridge chip (CP2102, CH340, FTDI): the computer sees a serial port, and two of its lines, " +
		"DTR and RTS, are wired to the BOOT and EN pins, so a program can reset the chip into download mode. The bridge has its own power, so the port " +
		"stays when the chip resets. Or through the chip's own USB (C3, C6, S3, H2…): no extra chip, but the USB is inside the ESP32, so the port " +
		"goes away for a moment on every reset.",
	ViaBridge: "Through a USB-UART bridge",
	ViaOwnUSB: "Through the chip's own USB",
	Computer:  "Computer",
	USBInside: "USB inside",
	PortGone:  "the port disappears",
	Resetting: "Reset",

	FamilyTitle: "The ESP32 family",
	FamilyText: "All the ESP32 chips share the tools, the start-up and the flash layout, but differ in cores, radio and USB. The ESP32, S2 and S3 " +
		"have Xtensa cores, the others RISC-V. The H-series have no Wi-Fi: Bluetooth LE and 802.15.4 (Thread, Zigbee). The P4 has no radio at all, " +
		"but fast cores and high-speed USB.",
	ColChip:  "Chip",
	ColCore:  "Cores",
	ColRadio: "Radio",

	PanicTitle: "Crashes and their decoding",
	PanicText: "When the firmware crashes (a panic), ESP-IDF prints the reason and the addresses where the code was: a backtrace on the Xtensa chips, " +
		"the registers and a stack dump on RISC-V. The addresses alone are numbers; with the .elf of the same build they become function names " +
		"and source lines. AltEsp's monitor does it by itself when it knows the .elf. A coredump saved to a partition is decoded the same way " +
		"later: Tools, Coredump analysis.",
	LogBox:    "Monitor",
	SameBuild: "of the same build",
	Decoded:   "Decoded with the .elf",
}

var guideRussian = Guide{
	Title: "Как устроен ESP32",
	Menu:  "Как устроен ESP32…",
	Prev:  "← Назад",
	Next:  "Дальше →",

	ChipTitle: "Что внутри чипа",
	ChipText: "ESP32 — семейство микроконтроллеров Espressif с Wi-Fi и Bluetooth. Внутри чипа: одно или два процессорных ядра; " +
		"ROM с заводским кодом, который никогда не меняется; SRAM для работающей программы и её данных; радио; периферия для связи с внешним миром " +
		"(выводы, UART, SPI, I²C, АЦП, USB); и eFuse — биты, которые записываются один раз. Сама программа хранится в отдельной микросхеме SPI flash, " +
		"часто в том же корпусе или модуле. Ядра выполняют код прямо из неё через кэш — это бегущие точки. На некоторых платах есть ещё PSRAM, " +
		"дополнительная память на той же шине.",
	ChipBox:     "Чип ESP32",
	CPU:         "Процессор\n1–2 ядра",
	Radio:       "Радио: Wi-Fi · Bluetooth",
	Cache:       "Кэш",
	Peripherals: "Периферия\nGPIO · UART · SPI · I²C · АЦП · USB",
	FlashChip:   "SPI flash\nпрограмма и данные",
	PSRAM:       "PSRAM\n(на некоторых платах)",

	MemoryTitle: "Flash и оперативная память",
	MemoryText: "Памяти два вида. SRAM быстрая, но всё забывает без питания: в ней то, что программа делает сейчас. Её несколько сотен килобайт. " +
		"Flash хранит данные без питания: обычно от 2 до 16 МБ, в ней программа и то, что должно пережить перезагрузку. Flash нельзя перезаписать " +
		"на месте: сначала стирается сектор 4 КБ (все байты становятся FF), потом в него пишут. Каждый сектор выдерживает около ста тысяч стираний, " +
		"поэтому прошивки пишут данные на новые места, а не в одно и то же.",
	PowerOn:   "Питание есть",
	PowerOff:  "Питания нет",
	RAMBox:    "SRAM",
	RAMNote:   "быстрая, сотни КБ, без питания стирается",
	FlashBox:  "Flash",
	FlashNote: "медленнее, мегабайты, без питания хранится",

	BootTitle: "Как плата запускается",
	BootText: "При включении чип выполняет код из своего ROM. Если в этот момент вывод BOOT прижат к земле (GPIO0 у ESP32 и серии S, GPIO9 у большинства " +
		"чипов серий C и H), ROM переходит в режим загрузки и ждёт команд по UART или USB: так прошивают чип AltEsp и другие программы. " +
		"Иначе ROM загружает из flash загрузчик второй ступени (по адресу 0x1000 у ESP32 и S2, 0x2000 у C5 и P4, 0x0 у остальных). " +
		"Загрузчик читает таблицу разделов, обычно по адресу 0x8000, выбирает приложение и запускает его.",
	ROMStep:        "ROM",
	InChip:         "внутри чипа",
	BootloaderStep: "Загрузчик",
	TableStep:      "Таблица\nразделов",
	AppStep:        "Приложение",
	DownloadStep:   "Режим загрузки:\nждёт программу",
	BootPin:        "BOOT зажат при сбросе: ROM ждёт команд вместо запуска загрузчика",

	MapTitle: "Карта flash и разделы",
	MapText: "Flash поделена на области. В начале — загрузчик и таблица разделов; таблица описывает остальные области, разделы: имя, тип " +
		"(приложение или данные), подтип, адрес и размер. Приложения лежат в разделах типа app, данные — в разделах типа data. " +
		"В AltEsp вкладка «Разделы» показывает таблицу платы, вкладка «Память» — её байты.",
	AreaText: []string{
		"Загрузчик второй ступени: его запускает ROM, он читает таблицу разделов и запускает приложение.",
		"Таблица разделов: где какой раздел и что в нём. Её читают загрузчик и приложение.",
		"NVS: настройки прошивки в виде ключей и значений — сеть Wi-Fi, счётчики, калибровка.",
		"otadata: какой слот OTA загружать. Маленький, два сектора: пока пишется один, второй цел.",
		"phy_init: данные калибровки радио.",
		"ota_0: слот приложения. Отсюда работает программа.",
		"ota_1: второй слот, сюда пишется обновление, пока работает ota_0.",
		"storage: файловая система для файлов прошивки — веб-страниц, настроек, журналов.",
	},

	NVSTitle: "NVS: настройки прошивки",
	NVSText: "NVS (энергонезависимое хранилище) держит небольшие настройки прошивки: сеть Wi-Fi и пароль, счётчики, калибровку. " +
		"Это пары «ключ — значение», собранные в пространства имён (пространство wifi, ключ ssid). Flash нельзя перезаписать на месте, поэтому " +
		"новое значение пишется в следующую свободную запись, а старое помечается стёртым. Когда страница заполнится, действующие значения " +
		"переносятся на чистую страницу, а старая стирается: износ распределяется по секторам. В AltEsp: «Разделы», правый щелчок по nvs — смотреть и править.",
	Page:     "Страница",
	NVSWrite: "Каждая запись идёт в следующую свободную ячейку; старое значение ключа помечается стёртым",
	NVSMove:  "Страница заполнена: действующие значения переезжают на чистую, старая стирается",

	OTATitle: "OTA: обновление без кабеля",
	OTAText: "OTA (over the air) обновляет прошивку по сети. Слотов для приложения два, ota_0 и ota_1. Пока работает один, новая версия " +
		"скачивается во второй. Затем otadata, маленький раздел, переключается на него, и чип перезапускается уже в новой версии. " +
		"Если включён откат, а новая версия не подтвердила, что работает, при следующем запуске вернётся старая. " +
		"В AltEsp вкладка «Разделы» отмечает загружаемый слот; переключить — правым щелчком.",
	Runs:        "работает",
	Downloading: "скачивается…",
	OTADownload: "Работает ota_0; новая версия скачивается в ota_1",
	OTASwitch:   "otadata теперь указывает на ota_1",
	OTARestart:  "После перезапуска загрузчик запускает ota_1",
	OTARollback: "Если v1.1 упадёт, не подтвердив работу, загрузчик вернёт ota_0",

	FSTitle: "Файловые системы",
	FSText: "Для файлов — веб-страниц, настроек, журналов — прошивка держит файловую систему в разделе данных. AltEsp открывает все три вида: " +
		"«Разделы», правый щелчок по разделу — посмотреть файлы, скачать, добавить, удалить и записать раздел обратно.",
	LittleFSNote: "LittleFS: переживает отключение питания посреди записи; есть папки; обычный выбор сегодня.",
	SPIFFSNote:   "SPIFFS: старая; настоящих папок нет, имена — это пути; медленнее по мере заполнения.",
	FATNote:      "FAT: файловая система карт памяти, поверх слоя выравнивания износа, который распределяет запись.",

	EFuseTitle: "eFuse",
	EFuseText: "eFuse — биты внутри чипа, которые записываются один раз. Бит начинается с 0, его можно «прожечь» в 1, но не обратно. " +
		"В них MAC-адрес, калибровка и ревизия чипа, а также настройки защиты: ключи шифрования flash и Secure Boot, переключатели, которые " +
		"отключают JTAG или режим загрузки. Ошибочный прожиг может навсегда сделать чип непригодным, поэтому AltEsp прожигает только в отдельном " +
		"режиме, после ввода слова BURN.",
	EFuseOnce: "Бит прожигается из 0 в 1 один раз и обратно не возвращается",

	SecurityTitle: "Шифрование и Secure Boot",
	SecurityText: "Шифрование flash: чип шифрует содержимое flash ключом, который хранится в его eFuse. Копию flash не прочитать без этого чипа, " +
		"а сам чип расшифровывает её на лету. Secure Boot: загрузчик и приложение подписаны, и чип запускает только то, что подписано вашим ключом, — " +
		"изменённая прошивка не запустится. Оба включаются прожигом eFuse и в основном не выключаются.",
	EncryptionBox:    "Шифрование flash",
	InChipPlain:      "В чипе, при работе:",
	KeyInEFuse:       "ключ в eFuse",
	InFlashEnc:       "Во flash:",
	SignedImage:      "образ подписан вашим ключом",
	ChangedImage:     "образ изменён или не подписан",
	BootloaderChecks: "проверяется подпись",
	Starts:           "запускается",
	Refused:          "не запускается",

	USBTitle: "Связь с компьютером",
	USBText: "Способов два. Через микросхему-мост USB-UART (CP2102, CH340, FTDI): компьютер видит последовательный порт, а две его линии, DTR и RTS, " +
		"подключены к выводам BOOT и EN — так программа сбрасывает чип в режим загрузки. Мост питается отдельно, поэтому порт остаётся, " +
		"когда чип перезагружается. Или через собственный USB чипа (C3, C6, S3, H2…): лишней микросхемы нет, но USB внутри ESP32, поэтому " +
		"при каждом сбросе порт на миг пропадает.",
	ViaBridge: "Через мост USB-UART",
	ViaOwnUSB: "Через собственный USB чипа",
	Computer:  "Компьютер",
	USBInside: "USB внутри",
	PortGone:  "порт пропадает",
	Resetting: "Сброс",

	FamilyTitle: "Семейство ESP32",
	FamilyText: "У всех чипов ESP32 общие инструменты, запуск и устройство flash, но разные ядра, радио и USB. У ESP32, S2 и S3 ядра Xtensa, " +
		"у остальных — RISC-V. У серии H нет Wi-Fi: Bluetooth LE и 802.15.4 (Thread, Zigbee). У P4 радио нет вовсе, зато быстрые ядра " +
		"и скоростной USB.",
	ColChip:  "Чип",
	ColCore:  "Ядра",
	ColRadio: "Радио",

	PanicTitle: "Падение и его расшифровка",
	PanicText: "Когда прошивка падает (паника), ESP-IDF печатает причину и адреса, где был код: у чипов Xtensa — backtrace, у RISC-V — регистры " +
		"и дамп стека. Сами по себе адреса — просто числа; с .elf той же сборки они превращаются в имена функций и строки исходников. " +
		"Монитор AltEsp делает это сам, если знает .elf. Coredump, сохранённый в раздел, расшифровывается так же позже: «Инструменты» → «Анализ coredump».",
	LogBox:    "Монитор",
	SameBuild: "той же сборки",
	Decoded:   "Расшифровано по .elf",
}

var guideCatalog = i18n.NewCatalog(guideEnglish, map[string]Guide{"ru": guideRussian})

// G is the text of the guide in the language of the interface
func G() *Guide { return guideCatalog.Get(ui.Language()) }
