//go:build windows

package main

// The status window: what restaurant staff see when they open the print
// agent on a till where it's installed. It shows each printer's state, the
// connection to Palate and what to do about a problem, refreshed every two
// seconds, and needs no administrator rights: it only reads the status file
// the agent writes and asks Windows whether the service is running. The
// setup window, which can change things, is one button away and asks for
// administrator rights as before.

import (
	"errors"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/agent"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/service"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/status"
)

var (
	pSetTimer       = user32.NewProc("SetTimer")
	pKillTimer      = user32.NewProc("KillTimer")
	pFlashWindowEx  = user32.NewProc("FlashWindowEx")
	pInvalidateRect = user32.NewProc("InvalidateRect")
)

const (
	wmTimer      = 0x113
	monitorTimer = 1
	refreshEvery = 2000 // ms

	idMonList = 300 + iota
	idMonSetup
	idMonClose

	rgbRed   = 0x0000B4 // COLORREF is 0x00BBGGRR
	rgbGreen = 0x00701A
)

type flashInfo struct {
	cbSize    uint32
	hwnd      uintptr
	dwFlags   uint32
	uCount    uint32
	dwTimeout uint32
}

type monitor struct {
	g       *gui // drawing helpers: scaling, fonts, controls
	cfgPath string

	hwnd, till, head, detail, list, advice, setup uintptr
	big                                           uintptr // the headline's font

	bad   bool
	down  map[string]bool
	lines []string
}

// runMonitor shows the status window until it's closed.
func runMonitor(cfgPath string, ownConsole bool) error {
	// A window belongs to the thread that made it, and only that thread
	// gets its messages: stay on one.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if ownConsole {
		if h, _, _ := pGetConsoleWindow.Call(); h != 0 {
			pShowWindow.Call(h, 0)
		}
		pFreeConsole.Call()
	}
	g := &gui{dpi: 96}
	g.initUI()
	m := &monitor{g: g, cfgPath: cfgPath, down: map[string]bool{}}
	m.big = g.makeFont(13, 600)
	if err := g.register(map[string]func(hwnd, m, w, l uintptr) uintptr{"PalateStatus": m.proc}); err != nil {
		return err
	}
	m.hwnd = g.window("PalateStatus", "Palate printers", 600, 440, 0)
	if m.hwnd == 0 {
		return errors.New("status window: couldn't create the window")
	}
	g.main = m.hwnd
	m.build()
	m.refresh()
	pSetTimer.Call(m.hwnd, monitorTimer, refreshEvery, 0)
	pShowWindow.Call(m.hwnd, 5)
	pSetForeground.Call(m.hwnd)

	var msg msg
	for {
		r, _, _ := pGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		if r, _, _ := pIsDialogMessage.Call(m.hwnd, uintptr(unsafe.Pointer(&msg))); r != 0 {
			continue
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		pDispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}
	return nil
}

func (m *monitor) build() {
	g, h := m.g, m.hwnd
	g.label(h, "Palate printers", 24, 16, 552, 34, g.title)
	m.till = g.label(h, "", 24, 52, 552, 20, 0)
	m.head = g.label(h, "", 24, 84, 552, 30, m.big)
	m.detail = g.label(h, "", 24, 118, 552, 40, 0)
	m.list = g.ctl(h, "LISTBOX", "", wsTabStop|wsVScroll|lbsNoIntegral, wsExClientEdge, idMonList, 24, 162, 552, 150, 0)
	m.advice = g.label(h, "", 24, 320, 552, 60, 0)
	m.setup = g.button(h, "Change setup...", idMonSetup, 24, 392, 150, false)
	g.button(h, "Close", idMonClose, 476, 392, 100, true)
}

// refresh reads the status again and redraws what changed. A printer that
// has just gone down, or a new problem, flashes the window in the taskbar.
func (m *monitor) refresh() {
	svc := "unknown"
	switch info, err := service.Status(); {
	case err == nil:
		svc = info.State
	case errors.Is(err, service.ErrNotInstalled):
		svc = ""
	}
	f, err := status.Read(status.PathFor(m.cfgPath))
	v := describeStatus(f, err, svc, time.Now())

	till := "Version " + agent.Version
	if err == nil && f.Till != "" {
		till = "Till " + f.Till + "   ·   version " + agent.Version
	}
	setText(m.till, till)
	setText(m.head, v.Headline)
	setText(m.detail, v.Detail)
	setText(m.advice, v.Advice)
	title := "Palate printers"
	if v.Bad {
		title = "Palate printers — " + v.Headline
	}
	setText(m.hwnd, title)

	var lines []string
	for _, l := range v.Lines {
		lines = append(lines, l.Text)
	}
	if strings.Join(lines, "\n") != strings.Join(m.lines, "\n") {
		send(m.list, lbResetContent, 0, 0)
		for _, l := range lines {
			send(m.list, lbAddString, 0, uintptr(unsafe.Pointer(wstr(l))))
		}
		m.lines = lines
	}

	alarm := v.Bad && !m.bad
	down := map[string]bool{}
	for _, n := range v.Down {
		down[n] = true
		if !m.down[n] {
			alarm = true
		}
	}
	m.bad, m.down = v.Bad, down
	if alarm {
		fi := flashInfo{hwnd: m.hwnd, dwFlags: 0x3 | 0xC} // caption and taskbar, until the window is brought forward
		fi.cbSize = uint32(unsafe.Sizeof(fi))
		pFlashWindowEx.Call(uintptr(unsafe.Pointer(&fi)))
	}
	// Repaint the headline in its new colour.
	pInvalidateRect.Call(m.head, 0, 1)
}

func (m *monitor) proc(hwnd, msgID, w, l uintptr) uintptr {
	switch msgID {
	case wmTimer:
		m.refresh()
		return 0
	case wmCommand:
		switch int(w & 0xFFFF) {
		case idMonClose:
			pDestroyWindow.Call(hwnd)
		case idMonSetup:
			if err := relaunchElevated([]string{"setup", "-config", m.cfgPath, "-gui"}); err != nil {
				if !errors.Is(err, windows.ERROR_CANCELLED) {
					messageBox(hwnd, "Setup couldn't open: "+err.Error(), "Palate printers", mbOK|mbIconErr)
				}
				return 0
			}
			pDestroyWindow.Call(hwnd)
		}
		return 0
	case wmCtlColorStatic:
		pSetBkMode.Call(w, 1)
		if l == m.head {
			c := rgbGreen
			if m.bad {
				c = rgbRed
			}
			pSetTextColor.Call(w, uintptr(c))
		}
		b, _, _ := pGetSysColorBrush.Call(colorWindow)
		return b
	case wmClose:
		pDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		pKillTimer.Call(hwnd, monitorTimer)
		pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProc.Call(hwnd, msgID, w, l)
	return r
}
