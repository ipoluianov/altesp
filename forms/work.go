package forms

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ipoluianov/altesp/config"
	"github.com/ipoluianov/altesp/internal/ops"
	"github.com/ipoluianov/nui/ui"
)

// The work with the board runs off the UI thread, one piece at a time: a
// job. It connects first when it needs download mode, and its result comes
// back to the UI thread through ui.Invoke.

// run starts a job. needBoard: the job needs the board in download mode,
// so the monitor gives the port up and the board is connected first. work
// gets the session (nil when it is not needed and there is none); done
// gets the error on the UI thread. It returns false when the job could not
// start (another one runs, no port).
func (m *MainForm) run(needBoard bool, work func(ctx context.Context, s *ops.Session) error, done func(err error)) bool {
	if m.busy {
		m.status.ShowMessage(T().Busy)
		return false
	}
	sess := m.sess
	var port ops.PortInfo
	if needBoard && sess == nil {
		p, ok := m.selectedPort()
		if !ok {
			m.status.ShowMessage(T().NoPortSelected)
			return false
		}
		port = p
		m.monitor.stop()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.busy, m.cancel = true, cancel
	m.updateState()
	opt := ops.Options{
		Reset:  m.resetFor(port),
		Baud:   config.Get().FlashBaud,
		NoStub: m.flash.noStub,
		Manual: func() { ui.Invoke(m.showManual) },
		Status: func(text string) {
			ui.Invoke(func() { m.status.SetState(text, colorWait, port.Short) })
		},
	}
	// A USB-JTAG entry connects over JTAG
	dev, overJTAG := m.jtagDevs[port.Name]
	go func() {
		// The session is used by one goroutine at a time: the job, or the
		// reading of the pages of the Memory tab
		m.sessMu.Lock()
		defer m.sessMu.Unlock()
		var err error
		if needBoard && sess == nil {
			if overJTAG {
				sess, err = ops.ConnectJTAG(ctx, dev, "", opt.Status)
			} else {
				sess, err = ops.Connect(ctx, port, opt)
			}
			if err == nil {
				s := sess
				ui.Invoke(func() { m.connected(s) })
			}
		}
		if err == nil && work != nil {
			err = work(ctx, sess)
		}
		// After a failure or a stop the loader may be anywhere: the next
		// job connects again
		dropped := false
		if err != nil && sess != nil {
			sess.Close(context.Background(), false)
			dropped = true
		}
		cancel()
		ui.Invoke(func() {
			m.busy, m.cancel = false, nil
			m.hideManual()
			if dropped && m.sess == sess {
				m.setSession(nil)
			}
			// The job may have changed the flash and the partitions
			if needBoard && m.sess != nil {
				m.board = m.sess.Board()
				m.boardChanged()
			}
			if done != nil {
				done(err)
			}
			m.updateState()
		})
	}()
	return true
}

// stopJob cancels the job running
func (m *MainForm) stopJob() {
	if m.cancel != nil {
		m.cancel()
	}
}

// connected takes the session of a board just connected
func (m *MainForm) connected(s *ops.Session) {
	m.setSession(s)
	m.board = s.Board()
	m.hasBoard = true
	m.boardChanged()
	m.showStatus()
	if notes := m.board.Notes; len(notes) > 0 {
		m.status.ShowMessage(notes[0])
	}
}

// setSession keeps the session of the window
func (m *MainForm) setSession(s *ops.Session) {
	m.sess = s
}

// connect connects to the board, or disconnects it (F5, the button); a
// job running is cancelled
func (m *MainForm) connect() {
	switch {
	case m.busy:
		m.stopJob()
	case m.sess != nil:
		m.disconnect(nil)
	default:
		m.run(true, nil, func(err error) { m.showError(T().Connect, err) })
	}
}

// showError tells about a failed job: in the status bar and, unless it was
// stopped by the user, in a message box
func (m *MainForm) showError(title string, err error) {
	if err == nil {
		return
	}
	if ctxErr(err) {
		m.status.ShowMessage(T().Stopped)
		return
	}
	m.status.ShowMessage(firstLine(err.Error()))
	ui.ShowMessageBox(m.Form.Panel(), title, err.Error())
}

// ctxErr tells whether the work was cancelled
func ctxErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// showManual asks the user to put the board into download mode by hand:
// the board has no auto-reset circuit, or it didn't work
func (m *MainForm) showManual() {
	m.manual.SetVisible(true)
}

func (m *MainForm) hideManual() {
	m.manual.SetVisible(false)
}

// manualBar is the line under the top bar that asks for the buttons of the
// board, with Cancel
func (m *MainForm) manualBar() ui.Widgeter {
	row := ui.NewHBox()
	row.SetPanelPadding(4)
	row.Add(fixedSpace(4))
	l := ui.NewLabel("")
	l.SetTextFunc(func() string { return T().ManualHint })
	l.SetForegroundColor(colorWait.get())
	ui.OnThemeChanged.Connect(func(bool) { l.SetForegroundColor(colorWait.get()) })
	row.Add(l)
	b := ui.NewButton("")
	b.SetTextFunc(func() string { return T().Cancel })
	b.OnClick.Connect(func(ui.Widgeter) { m.stopJob() })
	row.Add(b)
	row.AddStretch()
	row.SetVisible(false)
	m.manual = row
	return row
}

// Ports

// portPoll is how often the ports are listed
const portPoll = 1000

// listPorts lists the ports in the background; a timer calls it
func (m *MainForm) listPorts() {
	if m.listing.Swap(true) {
		return
	}
	go func() {
		ports, devs := listAll()
		// The window is touched only when the list changed
		sig := fmt.Sprint(ports, devs)
		if sig == m.lastList.Load() {
			m.listing.Store(false)
			return
		}
		m.lastList.Store(sig)
		ui.Invoke(func() {
			m.listing.Store(false)
			m.setPorts(ports, devs)
		})
	}()
}

// listAll lists the serial ports and the chips' USB-JTAG devices
func listAll() ([]ops.PortInfo, []ops.JTAGDevice) {
	ports, _ := ops.Ports()
	devs, _ := ops.JTAGDevices()
	return ports, devs
}

// listPortsNow lists the ports at once
func (m *MainForm) listPortsNow() {
	ports, devs := listAll()
	m.lastList.Store(fmt.Sprint(ports, devs))
	m.setPorts(ports, devs)
}

// The USB-JTAG of a chip is an entry of the port list of its own: a port
// named "jtag:<serial>"

// jtagPrefix starts the name of a USB-JTAG entry
const jtagPrefix = "jtag:"

// isJTAG tells a USB-JTAG entry
func isJTAG(p ops.PortInfo) bool { return strings.HasPrefix(p.Name, jtagPrefix) }

// jtagEntry is the entry of a USB-JTAG device
func jtagEntry(d ops.JTAGDevice) ops.PortInfo {
	return ops.PortInfo{Name: jtagPrefix + d.Serial, Short: "USB-JTAG " + d.Serial, Description: d.Product,
		USB: true, VID: d.VID, PID: d.PID, SerialNumber: d.Serial, Location: d.Location}
}

// portKey tells the entries apart across reconnections; the USB-JTAG of a
// chip has the IDs of its serial port, so it has a key of its own
func portKey(p ops.PortInfo) string {
	if isJTAG(p) {
		return p.Key() + "|jtag"
	}
	return p.Key()
}

// portTitle is the entry as the list shows it
func portTitle(p ops.PortInfo) string {
	if isJTAG(p) {
		return "USB-JTAG · " + p.SerialNumber
	}
	return p.Title()
}

// setPorts puts the ports and the USB-JTAG devices in the list. The entry
// selected stays; at the start the one used last is selected, or the only
// board.
func (m *MainForm) setPorts(ports []ops.PortInfo, devs []ops.JTAGDevice) {
	m.jtagDevs = map[string]ops.JTAGDevice{}
	ports = append([]ops.PortInfo(nil), ports...)
	for _, d := range devs {
		e := jtagEntry(d)
		m.jtagDevs[e.Name] = d
		ports = append(ports, e)
	}
	m.ports = ports
	key := portKey(m.sel)
	if !m.hasSel {
		key = config.Get().LastPort
	}
	found := false
	for _, p := range ports {
		if portKey(p) == key {
			m.sel, m.hasSel, found = p, true, true
			break
		}
	}
	// The port of the board at work may be away for a moment: a reset
	if !found && !m.portInUse() {
		m.hasSel = false
		var boards []ops.PortInfo
		for _, p := range ports {
			if ops.LikelyBoard(p) && !isJTAG(p) {
				boards = append(boards, p)
			}
		}
		// A board seen over USB-JTAG only
		if len(boards) == 0 && len(devs) == 1 {
			boards = append(boards, jtagEntry(devs[0]))
		}
		if len(boards) == 1 {
			m.choosePort(boards[0])
		}
	}
	m.fillPortCombo()
	m.flash.showNoPorts(len(ports) == 0)
	m.updateState()
}

// portInUse tells whether the selected port is used: connected, monitored
// or at work
func (m *MainForm) portInUse() bool {
	return m.busy || m.sess != nil || m.monitor.active()
}

// fillPortCombo shows the ports in the list, the selected one chosen
func (m *MainForm) fillPortCombo() {
	m.portCombo.Clear()
	if len(m.ports) == 0 && !m.hasSel {
		m.portCombo.AddItem(T().NoPorts, nil)
		m.portCombo.SetSelectedIndex(0)
		return
	}
	index := -1
	for i, p := range m.ports {
		m.portCombo.AddItem(portTitle(p), i)
		if m.hasSel && portKey(p) == portKey(m.sel) {
			index = i
		}
	}
	if m.hasSel && index < 0 {
		// Away for a moment: still shown
		m.portCombo.AddItem(portTitle(m.sel), -1)
		index = len(m.ports)
	}
	m.portCombo.SetSelectedIndex(index)
}

// portChosen takes the port the user chose in the list
func (m *MainForm) portChosen() {
	i, ok := m.portCombo.ItemData(m.portCombo.SelectedIndex()).(int)
	if !ok || i < 0 || i >= len(m.ports) {
		return
	}
	m.choosePort(m.ports[i])
	m.updateState()
}

// choosePort selects the port: another board, so the card and the monitor
// of the last one go
func (m *MainForm) choosePort(p ops.PortInfo) {
	if m.hasSel && portKey(m.sel) == portKey(p) {
		m.sel = p
		return
	}
	m.monitor.stop()
	m.sel, m.hasSel = p, true
	m.hasBoard = false
	m.showBoard()
	config.Update(func(s *config.Settings) { s.LastPort = portKey(p) })
	m.openMonitorIfShown()
}

// selectedPort returns the port selected
func (m *MainForm) selectedPort() (ops.PortInfo, bool) {
	return m.sel, m.hasSel
}

// reappear finds the port of a board with built-in USB after its reset:
// it goes away for a moment and comes back, maybe under another name. A
// port that doesn't go away is taken as it is.
func reappear(ctx context.Context, p ops.PortInfo) (ops.PortInfo, error) {
	if !ops.BuiltinUSB(p) {
		return p, nil
	}
	gone, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	q, err := ops.WaitPort(gone, p, true)
	cancel()
	if err == nil {
		return q, nil
	}
	if ctx.Err() != nil {
		return p, ctx.Err()
	}
	back, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return ops.WaitPort(back, p, false)
}

// portText is the port as the status bar shows it: its name, its speed and
// the loader; a port of the chip's own USB has no speed
func portText(p ops.PortInfo, baud int, loader string) string {
	parts := []string{p.Short}
	if ops.BuiltinUSB(p) {
		parts = append(parts, "USB")
	} else if baud > 0 {
		parts = append(parts, fmt.Sprintf(T().BaudFmt, baud))
	}
	if loader != "" {
		parts = append(parts, loader)
	}
	return strings.Join(parts, " · ")
}
