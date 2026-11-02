package forms

import (
	"github.com/ipoluianov/nui/ui"
	"github.com/ipoluianov/nui/ui/i18n"
)

// Strings are the texts of the interface in one language
type Strings struct {
	// Top bar
	PortTip, NoPorts, Connect, ConnectTip string
	Tools, MenuTip                        string

	// Board card and recent projects
	Board, NoBoard, NoBoardHint string
	RecentProjects, NoProjects  string

	// Tabs
	TabFlash, TabMemory, TabPartitions, TabMonitor     string
	HintFlash, HintMemory, HintPartitions, HintMonitor string

	// Tools menu
	ToolEfuse, ToolPartitionEditor, ToolImages, ToolCoredump string

	// State
	NotConnected, NoPortSelected, NotYet string

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
	PortTip:    "The port of the board",
	NoPorts:    "No ports",
	Connect:    "Connect",
	ConnectTip: "Reset the board into download mode and read the chip",
	Tools:      "Tools",
	MenuTip:    "Menu",

	Board:          "Board",
	NoBoard:        "No board connected",
	NoBoardHint:    "Choose the port\nand press Connect",
	RecentProjects: "Recent projects",
	NoProjects:     "The folders you flash from\nappear here",

	TabFlash:       "Flash",
	TabMemory:      "Memory",
	TabPartitions:  "Partitions",
	TabMonitor:     "Monitor",
	HintFlash:      "Drop the firmware files or the build folder here",
	HintMemory:     "The flash of the board in hex, read as you scroll",
	HintPartitions: "The partition table and what is in each partition",
	HintMonitor:    "The log of the board, with the panics decoded",

	ToolEfuse:           "eFuse",
	ToolPartitionEditor: "Partition table editor",
	ToolImages:          "Image generator",
	ToolCoredump:        "Coredump analysis",

	NotConnected:   "Not connected",
	NoPortSelected: "No port selected",
	NotYet:         "Not available yet",

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
	PortTip:    "Порт платы",
	NoPorts:    "Нет портов",
	Connect:    "Подключить",
	ConnectTip: "Перевести плату в режим загрузки и прочитать чип",
	Tools:      "Инструменты",
	MenuTip:    "Меню",

	Board:          "Плата",
	NoBoard:        "Плата не подключена",
	NoBoardHint:    "Выберите порт\nи нажмите «Подключить»",
	RecentProjects: "Недавние проекты",
	NoProjects:     "Здесь появятся папки,\nиз которых вы прошиваете",

	TabFlash:       "Прошивка",
	TabMemory:      "Память",
	TabPartitions:  "Разделы",
	TabMonitor:     "Монитор",
	HintFlash:      "Перетащите сюда файлы прошивки или папку сборки",
	HintMemory:     "Flash платы в hex, читается по мере прокрутки",
	HintPartitions: "Таблица разделов и то, что лежит в каждом разделе",
	HintMonitor:    "Лог платы с расшифровкой паник",

	ToolEfuse:           "eFuse",
	ToolPartitionEditor: "Редактор таблицы разделов",
	ToolImages:          "Генератор образов",
	ToolCoredump:        "Анализ coredump",

	NotConnected:   "Не подключено",
	NoPortSelected: "Порт не выбран",
	NotYet:         "Пока недоступно",

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
