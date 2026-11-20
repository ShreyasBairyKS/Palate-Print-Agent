//go:build windows

package main

// The setup window: a small native Windows window, drawn with Windows' own
// controls and theme. It adds no dependency, opens no port and starts no
// other program; every step it takes is the same code the console setup and
// the "printer" commands use (setupflow.go).

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/agent"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/config"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/pair"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/printer/discover"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/status"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	comctl32 = windows.NewLazySystemDLL("comctl32.dll")

	pRegisterClassEx   = user32.NewProc("RegisterClassExW")
	pCreateWindowEx    = user32.NewProc("CreateWindowExW")
	pDefWindowProc     = user32.NewProc("DefWindowProcW")
	pGetMessage        = user32.NewProc("GetMessageW")
	pTranslateMessage  = user32.NewProc("TranslateMessage")
	pDispatchMessage   = user32.NewProc("DispatchMessageW")
	pIsDialogMessage   = user32.NewProc("IsDialogMessageW")
	pPostMessage       = user32.NewProc("PostMessageW")
	pSendMessage       = user32.NewProc("SendMessageW")
	pShowWindow        = user32.NewProc("ShowWindow")
	pDestroyWindow     = user32.NewProc("DestroyWindow")
	pPostQuitMessage   = user32.NewProc("PostQuitMessage")
	pSetWindowText     = user32.NewProc("SetWindowTextW")
	pGetWindowText     = user32.NewProc("GetWindowTextW")
	pGetWindowTextLen  = user32.NewProc("GetWindowTextLengthW")
	pEnableWindow      = user32.NewProc("EnableWindow")
	pMessageBox        = user32.NewProc("MessageBoxW")
	pLoadCursor        = user32.NewProc("LoadCursorW")
	pLoadIcon          = user32.NewProc("LoadIconW")
	pSetFocus          = user32.NewProc("SetFocus")
	pGetSystemMetrics  = user32.NewProc("GetSystemMetrics")
	pAdjustWindowRect  = user32.NewProc("AdjustWindowRectEx")
	pSetProcessDPIAwar = user32.NewProc("SetProcessDPIAware")
	pGetDpiForSystem   = user32.NewProc("GetDpiForSystem")
	pGetSysColorBrush  = user32.NewProc("GetSysColorBrush")
	pSetForeground     = user32.NewProc("SetForegroundWindow")
	pCreateFont        = gdi32.NewProc("CreateFontW")
	pSetTextColor      = gdi32.NewProc("SetTextColor")
	pSetBkMode         = gdi32.NewProc("SetBkMode")
	pGetConsoleWindow  = kernel32.NewProc("GetConsoleWindow")
	pFreeConsole       = kernel32.NewProc("FreeConsole")
	pCreateActCtx      = kernel32.NewProc("CreateActCtxW")
	pActivateActCtx    = kernel32.NewProc("ActivateActCtx")
	pInitCommonCtrls   = comctl32.NewProc("InitCommonControlsEx")
)

const (
	wsCaption, wsSysMenu, wsMinimize         = 0x00C00000, 0x00080000, 0x00020000
	wsVisible, wsChild, wsTabStop, wsGroup   = 0x10000000, 0x40000000, 0x00010000, 0x00020000
	wsVScroll, wsDisabled                    = 0x00200000, 0x08000000
	wsExClientEdge, wsExControlParent        = 0x200, 0x10000
	wsExDlgModalFrame                        = 0x1
	esAutoHScroll, esMultiline, esReadOnly   = 0x80, 0x4, 0x800
	esAutoVScroll, esUppercase               = 0x40, 0x8
	bsDefPush, bsAutoRadio                   = 0x1, 0x9
	lbsNotify, lbsNoIntegral                 = 0x1, 0x100
	cbsDropDownList                          = 0x3
	wmCreate, wmDestroy, wmClose, wmCommand  = 0x1, 0x2, 0x10, 0x111
	wmSetFont, wmCtlColorStatic, wmApp       = 0x30, 0x138, 0x8000
	bmGetCheck, bmSetCheck                   = 0xF0, 0xF1
	lbAddString, lbResetContent, lbGetCurSel = 0x180, 0x184, 0x188
	lbSetCurSel                              = 0x186
	cbAddString, cbResetContent, cbGetCurSel = 0x143, 0x14B, 0x147
	cbSetCurSel                              = 0x14E
	emSetSel, emReplaceSel, emSetReadOnly    = 0xB1, 0xC2, 0xCF
	emSetCueBanner                           = 0x1501
	enChange, lbnDblClk                      = 0x300, 2
	mbOK, mbYesNo, mbIconErr, mbIconWarn     = 0x0, 0x4, 0x10, 0x30
	mbIconInfo, idYes                        = 0x40, 6
	colorWindow                              = 5
	wmUser                                   = wmApp + 1
)

// Control ids.
const (
	idServer = 100 + iota
	idPrinters
	idAdd
	idEdit
	idRemove
	idTest
	idCode
	idInstall
	idStatus
	idName
	idID
	idNetwork
	idIP
	idUSB
	idUSBList
	idWidth
	idBackup
	idOK
	idCancel
	idDlgError
	idFind
	idFindList
	idFindAgain
	idFindSlips
	idFindAdd
	idFindClose
)

type wndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type msg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      [2]int32
	private uint32
}

type rect struct{ left, top, right, bottom int32 }

type actCtx struct {
	cbSize                 uint32
	dwFlags                uint32
	lpSource               *uint16
	wProcessorArchitecture uint16
	wLangID                uint16
	lpAssemblyDirectory    *uint16
	lpResourceName         uintptr
	lpApplicationName      *uint16
	hModule                uintptr
}

func wstr(s string) *uint16 { p, _ := windows.UTF16PtrFromString(s); return p }

func send(h uintptr, m uint32, w, l uintptr) uintptr {
	r, _, _ := pSendMessage.Call(h, uintptr(m), w, l)
	return r
}

func getText(h uintptr) string {
	n, _, _ := pGetWindowTextLen.Call(h)
	buf := make([]uint16, n+1)
	pGetWindowText.Call(h, uintptr(unsafe.Pointer(&buf[0])), n+1)
	return syscall.UTF16ToString(buf)
}

func setText(h uintptr, s string) { pSetWindowText.Call(h, uintptr(unsafe.Pointer(wstr(s)))) }

func enable(h uintptr, on bool) {
	v := uintptr(0)
	if on {
		v = 1
	}
	pEnableWindow.Call(h, v)
}

func messageBox(owner uintptr, text, title string, flags uintptr) int {
	r, _, _ := pMessageBox.Call(owner, uintptr(unsafe.Pointer(wstr(text))), uintptr(unsafe.Pointer(wstr(title))), flags)
	return int(r)
}

// ---------------------------------------------------------------------------

type guiPrinter struct {
	p    config.Printer
	test string // "", "testing", "ok", or why it failed
}

type gui struct {
	env       *setupEnv
	installed bool // the service exists on this PC
	paired    bool
	state     string // service state, when installed
	raw       *config.Config
	printers  []guiPrinter
	usb       []string
	busy      bool

	dpi        int
	font, bold uintptr
	title      uintptr
	hinst      uintptr

	main, server, list, add, edit, remove, test, find, code, codeLabel, install, status, subtitle uintptr

	// Finding network printers, and the Find printers dialog while it's open.
	searching, searched, identifying bool
	found                            []discover.Found
	nets                             []discover.Network
	findErr                          error
	prefill                          *discover.Found // a found printer the printer dialog opens with
	fdlg, fStatus, fList             uintptr
	fAgain, fSlips, fAdd             uintptr

	// The printer dialog, while it's open.
	dlg, dName, dID, dNet, dIP, dUSB, dUSBList, dWidth, dBackup, dError uintptr
	dEditing                                                            string // id of the printer being edited, or ""
	dIDTouched, dSettingID                                              bool
	dBackups                                                            []string

	// What the running agent last said about each printer, once installed.
	live map[string]status.Printer

	mu    sync.Mutex
	queue []func()
}

var theGUI *gui

func (g *gui) s(v int) int { return v * g.dpi / 96 }

// later runs f on the window's thread: background work calls it to update
// the window.
func (g *gui) later(f func()) {
	g.mu.Lock()
	g.queue = append(g.queue, f)
	g.mu.Unlock()
	pPostMessage.Call(g.main, wmUser, 0, 0)
}

func (g *gui) drain() {
	g.mu.Lock()
	q := g.queue
	g.queue = nil
	g.mu.Unlock()
	for _, f := range q {
		f()
	}
}

// runGUI shows the setup window until it is closed.
func runGUI(e *setupEnv, ownConsole bool) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if ownConsole {
		// Double-clicked: hide the black console window behind us.
		if h, _, _ := pGetConsoleWindow.Call(); h != 0 {
			pShowWindow.Call(h, 0)
		}
		pFreeConsole.Call()
	}

	g := &gui{env: e, dpi: 96}
	theGUI = g
	g.initUI()
	g.load()
	if err := g.register(map[string]func(hwnd, m, w, l uintptr) uintptr{
		"PalateSetup": g.mainProc, "PalatePrinter": g.dlgProc, "PalateFind": g.findProc,
	}); err != nil {
		return fmt.Errorf("setup window: %v", err)
	}

	title := "Palate print agent setup"
	if g.installed {
		title = "Palate print agent"
	}
	g.main = g.window("PalateSetup", title, 600, 640, 0)
	if g.main == 0 {
		return errors.New("setup window: couldn't create the window")
	}
	g.build()
	pShowWindow.Call(g.main, 5)
	pSetForeground.Call(g.main)

	var m msg
	for {
		r, _, _ := pGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		if g.dlg != 0 {
			if r, _, _ := pIsDialogMessage.Call(g.dlg, uintptr(unsafe.Pointer(&m))); r != 0 {
				continue
			}
		}
		if g.fdlg != 0 {
			if r, _, _ := pIsDialogMessage.Call(g.fdlg, uintptr(unsafe.Pointer(&m))); r != 0 {
				continue
			}
		}
		if r, _, _ := pIsDialogMessage.Call(g.main, uintptr(unsafe.Pointer(&m))); r != 0 {
			continue
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
	return nil
}

// initUI gets the process ready to draw windows: the current theme, the
// screen's scaling, and the fonts.
func (g *gui) initUI() {
	g.useVisualStyles()
	pSetProcessDPIAwar.Call()
	if pGetDpiForSystem.Find() == nil {
		if d, _, _ := pGetDpiForSystem.Call(); d >= 96 {
			g.dpi = int(d)
		}
	}
	h, _, _ := kernel32.NewProc("GetModuleHandleW").Call(0)
	g.hinst = h
	g.font = g.makeFont(9, 400)
	g.bold = g.makeFont(9, 600)
	g.title = g.makeFont(15, 600)
}

// register creates the window classes, each with its message handler.
func (g *gui) register(classes map[string]func(hwnd, m, w, l uintptr) uintptr) error {
	cursor, _, _ := pLoadCursor.Call(0, 32512)
	icon, _, _ := pLoadIcon.Call(0, 32512)
	bg, _, _ := pGetSysColorBrush.Call(colorWindow)
	for name, proc := range classes {
		wc := wndClassEx{lpfnWndProc: windows.NewCallback(proc), hInstance: g.hinst, hIcon: icon, hIconSm: icon,
			hCursor: cursor, hbrBackground: bg, lpszClassName: wstr(name)}
		wc.cbSize = uint32(unsafe.Sizeof(wc))
		if r, _, err := pRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
			return err
		}
	}
	return nil
}

// useVisualStyles turns on Windows' current control theme (rounded buttons,
// placeholder text) without a manifest, the way .NET's EnableVisualStyles
// does: by activating the theme manifest Windows keeps in shell32.dll.
func (g *gui) useVisualStyles() {
	sys, _ := windows.GetSystemDirectory()
	ac := actCtx{dwFlags: 0x4 | 0x8, lpSource: wstr(filepath.Join(sys, "shell32.dll")),
		lpAssemblyDirectory: wstr(sys), lpResourceName: 124}
	ac.cbSize = uint32(unsafe.Sizeof(ac))
	if h, _, _ := pCreateActCtx.Call(uintptr(unsafe.Pointer(&ac))); h != 0 && h != ^uintptr(0) {
		var cookie uintptr
		pActivateActCtx.Call(h, uintptr(unsafe.Pointer(&cookie)))
	}
	icc := [2]uint32{8, 0xFF}
	pInitCommonCtrls.Call(uintptr(unsafe.Pointer(&icc[0])))
}

func (g *gui) makeFont(points, weight int) uintptr {
	f, _, _ := pCreateFont.Call(uintptr(-int32(points*g.dpi/72)), 0, 0, 0, uintptr(weight), 0, 0, 0, 1, 0, 0, 5, 0,
		uintptr(unsafe.Pointer(wstr("Segoe UI"))))
	return f
}

// window creates a top-level window with this client size, centred.
func (g *gui) window(class, title string, w, h int, owner uintptr) uintptr {
	style := uintptr(wsCaption | wsSysMenu | wsMinimize)
	ex := uintptr(wsExControlParent)
	if owner != 0 {
		style = wsCaption | wsSysMenu
		ex |= wsExDlgModalFrame
	}
	r := rect{0, 0, int32(g.s(w)), int32(g.s(h))}
	pAdjustWindowRect.Call(uintptr(unsafe.Pointer(&r)), style, 0, ex)
	ww, wh := r.right-r.left, r.bottom-r.top
	sw, _, _ := pGetSystemMetrics.Call(0)
	sh, _, _ := pGetSystemMetrics.Call(1)
	x, y := (int32(sw)-ww)/2, (int32(sh)-wh)/2
	hwnd, _, _ := pCreateWindowEx.Call(ex, uintptr(unsafe.Pointer(wstr(class))), uintptr(unsafe.Pointer(wstr(title))),
		style, uintptr(x), uintptr(y), uintptr(ww), uintptr(wh), owner, 0, g.hinst, 0)
	return hwnd
}

// ctl creates a child control at 96-dpi coordinates.
func (g *gui) ctl(parent uintptr, class, text string, style uintptr, ex uintptr, id, x, y, w, h int, font uintptr) uintptr {
	hwnd, _, _ := pCreateWindowEx.Call(ex, uintptr(unsafe.Pointer(wstr(class))), uintptr(unsafe.Pointer(wstr(text))),
		wsChild|wsVisible|style, uintptr(g.s(x)), uintptr(g.s(y)), uintptr(g.s(w)), uintptr(g.s(h)), parent, uintptr(id), g.hinst, 0)
	if font == 0 {
		font = g.font
	}
	send(hwnd, wmSetFont, font, 1)
	return hwnd
}

func (g *gui) label(parent uintptr, text string, x, y, w, h int, font uintptr) uintptr {
	return g.ctl(parent, "STATIC", text, 0, 0, 0, x, y, w, h, font)
}

func (g *gui) editBox(parent uintptr, id, x, y, w int, cue string, extra uintptr) uintptr {
	e := g.ctl(parent, "EDIT", "", wsTabStop|esAutoHScroll|extra, wsExClientEdge, id, x, y, w, 26, 0)
	if cue != "" {
		send(e, emSetCueBanner, 1, uintptr(unsafe.Pointer(wstr(cue))))
	}
	return e
}

func (g *gui) button(parent uintptr, text string, id, x, y, w int, def bool) uintptr {
	st := uintptr(wsTabStop)
	if def {
		st |= bsDefPush
	}
	return g.ctl(parent, "BUTTON", text, st, 0, id, x, y, w, 30, 0)
}

// ---------------------------------------------------------------------------
// Data

func (g *gui) load() {
	if info, err := g.env.status(); err == nil {
		g.installed, g.state = true, info.State
	}
	all, _ := g.env.usbList()
	g.usb = realPrinters(all)
	g.raw = &config.Config{}
	if c, err := config.Read(g.env.cfgPath); err == nil {
		g.raw = c
		for _, p := range c.Printers {
			g.printers = append(g.printers, guiPrinter{p: p})
		}
		if full, err := config.Load(g.env.cfgPath); err == nil {
			if _, err := (pair.FileStore{Path: full.CredentialFile}).Load(); err == nil {
				g.paired = true
			}
		}
	}
}

func (g *gui) syncRaw() {
	g.raw.Printers = nil
	for _, gp := range g.printers {
		g.raw.Printers = append(g.raw.Printers, gp.p)
	}
}

// ---------------------------------------------------------------------------
// Main window

func (g *gui) build() {
	m := g.main
	g.label(m, "Palate print agent", 24, 16, 400, 34, g.title)
	sub := "Connect this till's printers to Palate. Version " + agent.Version + "."
	if g.installed {
		sub = fmt.Sprintf("Installed on this PC and %s.", g.state)
		if !g.paired {
			sub = "Installed on this PC, but not paired with Palate yet."
		}
	}
	g.subtitle = g.label(m, sub, 24, 52, 552, 20, 0)

	g.label(m, "1   Palate's server address", 24, 88, 552, 20, g.bold)
	g.server = g.editBox(m, idServer, 24, 110, 552, "https://", 0)
	setText(g.server, g.raw.Server)
	if g.installed {
		send(g.server, emSetReadOnly, 1, 0)
	}

	g.label(m, "2   Printers on this till", 24, 152, 552, 20, g.bold)
	g.list = g.ctl(m, "LISTBOX", "", wsTabStop|wsVScroll|lbsNotify|lbsNoIntegral, wsExClientEdge, idPrinters, 24, 174, 552, 148, 0)
	g.add = g.button(m, "Add printer...", idAdd, 24, 330, 120, false)
	g.edit = g.button(m, "Edit...", idEdit, 150, 330, 90, false)
	g.remove = g.button(m, "Remove", idRemove, 246, 330, 90, false)
	g.test = g.button(m, "Test print", idTest, 342, 330, 100, false)
	g.find = g.button(m, "Find printers...", idFind, 448, 330, 128, false)

	g.codeLabel = g.label(m, "3   Pairing code from the Palate portal (\"Add a till\")", 24, 378, 552, 20, g.bold)
	g.code = g.editBox(m, idCode, 24, 400, 200, "K7Q2-M9XD", esUppercase)

	btn := "Install and start"
	switch {
	case g.installed && g.paired:
		btn = "Close"
	case g.installed:
		btn = "Pair and start"
	}
	g.install = g.button(m, btn, idInstall, 24, 446, 180, true)
	if g.installed && g.paired {
		pShowWindow.Call(g.codeLabel, 0)
		pShowWindow.Call(g.code, 0)
	}

	g.label(m, "Progress", 24, 494, 552, 20, g.bold)
	g.status = g.ctl(m, "EDIT", "", wsVScroll|esMultiline|esReadOnly|esAutoVScroll, wsExClientEdge, idStatus, 24, 516, 552, 104, 0)

	if g.installed {
		g.say("Changes to printers are test-printed, saved, and applied by restarting the service.")
	} else {
		if _, err := g.env.edgeFind(); err != nil {
			g.say("Note: Microsoft Edge wasn't found, so customer bills can't print until it's installed.")
		}
		if len(g.usb) == 0 {
			g.say("No USB receipt printers found.")
		}
	}
	if g.installed {
		g.watchLive()
	}
	g.refreshList()
	if !g.installed && len(g.printers) == 0 {
		g.autoSearch()
	}
	if g.raw.Server == "" {
		pSetFocus.Call(g.server)
	}
}

func (g *gui) say(line string) {
	n, _, _ := pGetWindowTextLen.Call(g.status)
	send(g.status, emSetSel, n, n)
	if n > 0 {
		line = "\r\n" + line
	}
	send(g.status, emReplaceSel, 0, uintptr(unsafe.Pointer(wstr(line))))
}

func (g *gui) refreshList() {
	sel := int(int32(send(g.list, lbGetCurSel, 0, 0)))
	send(g.list, lbResetContent, 0, 0)
	for _, gp := range g.printers {
		p := gp.p
		where := p.Address
		if p.Kind == "usb" {
			where = "USB: " + p.PrinterName
		}
		mark := ""
		live, haveLive := g.live[p.ID]
		switch {
		case gp.test == "" && haveLive && live.State == "up":
			mark = "   ·   ✓ working"
		case gp.test == "" && haveLive && live.State == "down":
			mark = "   ·   ✗ NOT WORKING: " + plainWhy(live.LastError)
		case gp.test == "testing":
			mark = "   ·   printing a test slip..."
		case gp.test == "ok":
			mark = "   ·   ✓ test printed"
		case gp.test != "":
			mark = "   ·   ✗ test failed"
		}
		backup := ""
		if p.Backup != "" {
			backup = "   ·   backup " + p.Backup
		}
		line := fmt.Sprintf("%s   (%s)   ·   %s   ·   %d mm%s%s", p.Name, p.ID, where, p.Width, backup, mark)
		send(g.list, lbAddString, 0, uintptr(unsafe.Pointer(wstr(line))))
	}
	if sel >= len(g.printers) {
		sel = len(g.printers) - 1
	}
	if sel < 0 && len(g.printers) > 0 {
		sel = 0
	}
	send(g.list, lbSetCurSel, uintptr(sel), 0)
	g.setBusy(g.busy)
}

func (g *gui) selected() int {
	i := int(int32(send(g.list, lbGetCurSel, 0, 0)))
	if i < 0 || i >= len(g.printers) {
		return -1
	}
	return i
}

func (g *gui) setBusy(b bool) {
	g.busy = b
	has := g.selected() >= 0
	enable(g.add, !b)
	enable(g.find, !b)
	enable(g.edit, !b && has)
	enable(g.remove, !b && has)
	enable(g.test, !b && has)
	enable(g.install, !b)
	enable(g.code, !b)
	if !g.installed {
		enable(g.server, !b)
	}
}

func (g *gui) mainProc(hwnd, m, w, l uintptr) uintptr {
	switch m {
	case wmCommand:
		id, code := int(w&0xFFFF), int(w>>16)
		switch {
		case id == idAdd:
			g.openPrinter(-1)
		case id == idFind:
			g.openFind()
		case id == idEdit || (id == idPrinters && code == lbnDblClk):
			if i := g.selected(); i >= 0 && !g.busy {
				g.openPrinter(i)
			}
		case id == idRemove:
			g.removePrinter()
		case id == idTest:
			if i := g.selected(); i >= 0 {
				g.testPrinter(g.printers[i].p.ID, nil)
			}
		case id == idInstall:
			g.installClicked()
		case id == idPrinters:
			g.setBusy(g.busy)
		}
		return 0
	case wmCtlColorStatic:
		pSetBkMode.Call(w, 1)
		b, _, _ := pGetSysColorBrush.Call(colorWindow)
		return b
	case wmUser:
		g.drain()
		return 0
	case wmTimer:
		if g.readLive() {
			g.refreshList()
		}
		return 0
	case wmClose:
		if g.busy {
			messageBox(hwnd, "Setup is still working. Wait for it to finish, then close the window.", "Palate print agent", mbOK|mbIconInfo)
			return 0
		}
		pDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProc.Call(hwnd, m, w, l)
	return r
}

// testPrinter prints a test slip on one printer in the background and marks
// the list. then, if given, runs on the window's thread with the result.
func (g *gui) testPrinter(id string, then func(err error)) {
	i := g.index(id)
	if i < 0 {
		return
	}
	p := g.printers[i].p
	// Test with the timings and normalised address the config would use.
	trial := *g.raw
	trial.Server = "https://example.invalid"
	g.syncRaw()
	trial.Printers = g.raw.Printers
	checked, err := trial.Check()
	if err != nil {
		messageBox(g.main, err.Error(), "Palate print agent", mbOK|mbIconErr)
		return
	}
	cp := *checked.Find(id)
	g.printers[i].test = "testing"
	g.refreshList()
	g.setBusy(true)
	g.say("Printing a test slip on " + p.Name + "...")
	go func() {
		err := g.env.testPrint(cp, checked.Timings)
		g.later(func() {
			if j := g.index(id); j >= 0 {
				if err == nil {
					g.printers[j].test = "ok"
					g.say("  " + p.Name + ": test slip printed. Check it's readable and the paper was cut.")
				} else {
					g.printers[j].test = err.Error()
					g.say("  " + p.Name + ": the test print failed: " + err.Error())
				}
			}
			g.setBusy(false)
			g.refreshList()
			if then != nil {
				then(err)
			}
		})
	}()
}

func (g *gui) index(id string) int {
	for i, gp := range g.printers {
		if gp.p.ID == id {
			return i
		}
	}
	return -1
}

func (g *gui) removePrinter() {
	i := g.selected()
	if i < 0 {
		return
	}
	p := g.printers[i].p
	for _, gp := range g.printers {
		if gp.p.Backup == p.ID {
			messageBox(g.main, fmt.Sprintf("%s uses %s as its backup. Change that first.", gp.p.Name, p.Name), "Palate print agent", mbOK|mbIconWarn)
			return
		}
	}
	if !g.installed {
		g.printers = append(g.printers[:i], g.printers[i+1:]...)
		g.refreshList()
		return
	}
	if len(g.printers) == 1 {
		messageBox(g.main, "It's the only printer on this till; a till needs at least one.", "Palate print agent", mbOK|mbIconWarn)
		return
	}
	if messageBox(g.main, "Remove "+p.Name+" from this till? Unlink it in Palate too, or its jobs will be recorded as failed.", "Palate print agent", mbYesNo|mbIconWarn) != idYes {
		return
	}
	g.printers = append(g.printers[:i], g.printers[i+1:]...)
	g.saveInstalledChange("removed " + p.Describe())
}

// saveInstalledChange writes the printers to the installed config and
// restarts the service, in the background.
func (g *gui) saveInstalledChange(what string) {
	raw, err := config.Read(g.env.cfgPath)
	if err != nil {
		messageBox(g.main, err.Error(), "Palate print agent", mbOK|mbIconErr)
		return
	}
	g.syncRaw()
	raw.Printers = g.raw.Printers
	if err := config.Save(g.env.cfgPath, raw); err != nil {
		messageBox(g.main, err.Error(), "Palate print agent", mbOK|mbIconErr)
		return
	}
	recordChange(g.env.cfgPath, what)
	g.raw = raw
	g.refreshList()
	g.say("Saved: " + what)
	g.setBusy(true)
	g.say("Restarting the service...")
	go func() {
		msg := restartService(g.env.cfgPath)
		g.later(func() { g.say("  " + msg); g.setBusy(false) })
	}()
}

func (g *gui) installClicked() {
	switch {
	case g.installed && g.paired:
		pDestroyWindow.Call(g.main)
		return
	case g.installed:
		g.pairAndStart()
		return
	}
	server := strings.TrimSpace(getText(g.server))
	if err := config.CheckServer(server); err != nil {
		messageBox(g.main, "Palate's server address: "+err.Error()+".", "Palate print agent", mbOK|mbIconWarn)
		pSetFocus.Call(g.server)
		return
	}
	if len(g.printers) == 0 {
		messageBox(g.main, "Add at least one printer first.", "Palate print agent", mbOK|mbIconWarn)
		return
	}
	var untested []string
	for _, gp := range g.printers {
		if gp.test != "ok" {
			untested = append(untested, gp.p.Name)
		}
	}
	if len(untested) > 0 && messageBox(g.main, "These printers haven't printed a test slip: "+strings.Join(untested, ", ")+
		".\n\nInstall anyway? Their tickets fail until they're reachable.", "Palate print agent", mbYesNo|mbIconWarn) != idYes {
		return
	}
	code := getText(g.code)
	if strings.TrimSpace(code) == "" && messageBox(g.main, "No pairing code. Install now and pair later?", "Palate print agent", mbYesNo|mbIconInfo) != idYes {
		pSetFocus.Call(g.code)
		return
	}
	g.raw.Server = server
	g.syncRaw()
	raw := *g.raw
	g.setBusy(true)
	go func() {
		paired, err := installSteps(g.env, &raw, code, func(s string) { g.later(func() { g.say(s) }) })
		g.later(func() {
			g.setBusy(false)
			if err != nil {
				g.say("Problem: " + err.Error())
				if errors.Is(err, config.ErrNeedAdmin) || strings.Contains(err.Error(), "administrator") {
					g.say("Close this window and double-click palate-agent.exe again, choosing Yes when Windows asks.")
				}
				messageBox(g.main, err.Error(), "Palate print agent", mbOK|mbIconErr)
				if info, serr := g.env.status(); serr == nil {
					g.becomeInstalled(info.State, paired)
				}
				return
			}
			g.becomeInstalled("running", paired)
			if paired {
				messageBox(g.main, "The print agent is installed and running. It starts with Windows, and tickets and bills print as soon as Palate sends them.", "Palate print agent", mbOK|mbIconInfo)
			}
		})
	}()
}

// watchLive shows the running agent's view of each printer in the list,
// read again every two seconds.
func (g *gui) watchLive() {
	g.readLive()
	pSetTimer.Call(g.main, monitorTimer, refreshEvery, 0)
}

// readLive reads the status file, and reports whether anything shown changed.
// A missing or old file shows nothing.
func (g *gui) readLive() bool {
	live := map[string]status.Printer{}
	if f, err := status.Read(status.PathFor(g.env.cfgPath)); err == nil && !f.Stale(time.Now()) && f.Stopped == "" {
		for _, p := range f.Printers {
			live[p.ID] = p
		}
	}
	changed := len(live) != len(g.live)
	for id, p := range live {
		if o, ok := g.live[id]; !ok || o.State != p.State || o.LastError != p.LastError {
			changed = true
		}
	}
	g.live = live
	return changed
}

func (g *gui) becomeInstalled(state string, paired bool) {
	if !g.installed {
		defer g.watchLive()
	}
	g.installed, g.paired, g.state = true, paired, state
	send(g.server, emSetReadOnly, 1, 0)
	if paired {
		setText(g.subtitle, "Installed on this PC and running.")
		setText(g.install, "Close")
		pShowWindow.Call(g.codeLabel, 0)
		pShowWindow.Call(g.code, 0)
	} else {
		setText(g.subtitle, "Installed on this PC, but not paired with Palate yet.")
		setText(g.install, "Pair and start")
	}
}

func (g *gui) pairAndStart() {
	code := strings.ToUpper(strings.TrimSpace(getText(g.code)))
	if code == "" {
		messageBox(g.main, "Type the pairing code from the Palate portal (\"Add a till\").", "Palate print agent", mbOK|mbIconInfo)
		pSetFocus.Call(g.code)
		return
	}
	cfg, err := config.Load(g.env.cfgPath)
	if err != nil {
		messageBox(g.main, err.Error(), "Palate print agent", mbOK|mbIconErr)
		return
	}
	g.setBusy(true)
	g.say("Pairing with Palate...")
	go func() {
		dev, err := g.env.pair(cfg, code)
		if err == nil {
			g.later(func() { g.say("Paired as " + dev + ". Starting the print agent...") })
			err = g.env.start()
		}
		g.later(func() {
			g.setBusy(false)
			if err != nil {
				g.say("Problem: " + err.Error())
				messageBox(g.main, err.Error(), "Palate print agent", mbOK|mbIconErr)
				return
			}
			g.say("Running. Tickets and bills print as soon as Palate sends them.")
			g.becomeInstalled("running", true)
		})
	}()
}

// ---------------------------------------------------------------------------
// The printer dialog

func (g *gui) openPrinter(i int) {
	if g.dlg != 0 || g.fdlg != 0 || g.busy {
		return
	}
	title, ok := "Add a printer", "Add"
	var p config.Printer
	g.dEditing = ""
	if i < 0 && g.prefill != nil {
		p.Kind, p.Address = "network", g.prefill.Addr
		title = "Add the printer at " + shortAddr(g.prefill.Addr)
		g.prefill = nil
	}
	if i >= 0 {
		p = g.printers[i].p
		g.dEditing = p.ID
		title, ok = "Edit "+p.Name, "Save"
	}
	g.dlg = g.window("PalatePrinter", title, 440, 380, g.main)
	d := g.dlg
	g.label(d, "Name, as the kitchen calls it", 24, 16, 392, 20, 0)
	g.dName = g.editBox(d, idName, 24, 38, 392, "e.g. Tandoor, Pass, Bill", 0)
	g.label(d, "Id (Palate's jobs use it; it never changes)", 24, 74, 392, 20, 0)
	g.dID = g.editBox(d, idID, 24, 96, 200, "e.g. p-tandoor", 0)

	g.label(d, "Connection", 24, 134, 392, 20, g.bold)
	g.dNet = g.ctl(d, "BUTTON", "Network printer", wsTabStop|wsGroup|bsAutoRadio, 0, idNetwork, 24, 158, 150, 24, 0)
	g.dIP = g.editBox(d, idIP, 180, 156, 236, "IP address, e.g. 192.168.1.50", 0)
	g.dUSB = g.ctl(d, "BUTTON", "USB printer", wsTabStop|bsAutoRadio, 0, idUSB, 24, 194, 150, 24, 0)
	g.dUSBList = g.ctl(d, "COMBOBOX", "", wsTabStop|wsVScroll|cbsDropDownList, 0, idUSBList, 180, 192, 236, 200, 0)
	for _, n := range g.usb {
		send(g.dUSBList, cbAddString, 0, uintptr(unsafe.Pointer(wstr(n))))
	}
	if len(g.usb) == 0 {
		send(g.dUSBList, cbAddString, 0, uintptr(unsafe.Pointer(wstr("(no USB receipt printers found)"))))
		enable(g.dUSB, false)
	}

	g.label(d, "Paper width", 24, 234, 150, 20, 0)
	g.dWidth = g.ctl(d, "COMBOBOX", "", wsTabStop|cbsDropDownList, 0, idWidth, 180, 230, 120, 120, 0)
	send(g.dWidth, cbAddString, 0, uintptr(unsafe.Pointer(wstr("80 mm"))))
	send(g.dWidth, cbAddString, 0, uintptr(unsafe.Pointer(wstr("58 mm"))))

	g.label(d, "Backup when it's down", 24, 270, 150, 20, 0)
	g.dBackup = g.ctl(d, "COMBOBOX", "", wsTabStop|wsVScroll|cbsDropDownList, 0, idBackup, 180, 266, 236, 160, 0)
	g.dBackups = []string{""}
	send(g.dBackup, cbAddString, 0, uintptr(unsafe.Pointer(wstr("None"))))
	for _, gp := range g.printers {
		if gp.p.ID != g.dEditing {
			g.dBackups = append(g.dBackups, gp.p.ID)
			send(g.dBackup, cbAddString, 0, uintptr(unsafe.Pointer(wstr(gp.p.Name+"  ("+gp.p.ID+")"))))
		}
	}

	g.dError = g.label(d, "", 24, 302, 392, 36, 0)
	g.button(d, "Cancel", idCancel, 230, 338, 90, false)
	g.button(d, ok, idOK, 326, 338, 90, true)

	// Fill in.
	g.dIDTouched = i >= 0
	setText(g.dName, p.Name)
	g.dSettingID = true
	setText(g.dID, p.ID)
	g.dSettingID = false
	if i >= 0 {
		send(g.dID, emSetReadOnly, 1, 0)
	}
	usb := p.Kind == "usb"
	if usb {
		send(g.dUSB, bmSetCheck, 1, 0)
		for k, n := range g.usb {
			if n == p.PrinterName {
				send(g.dUSBList, cbSetCurSel, uintptr(k), 0)
			}
		}
	} else {
		send(g.dNet, bmSetCheck, 1, 0)
		setText(g.dIP, strings.TrimSuffix(p.Address, ":9100"))
		send(g.dUSBList, cbSetCurSel, 0, 0)
	}
	if p.Width == 58 {
		send(g.dWidth, cbSetCurSel, 1, 0)
	} else {
		send(g.dWidth, cbSetCurSel, 0, 0)
	}
	send(g.dBackup, cbSetCurSel, 0, 0)
	for k, id := range g.dBackups {
		if id != "" && id == p.Backup {
			send(g.dBackup, cbSetCurSel, uintptr(k), 0)
		}
	}
	g.syncConnection()

	enable(g.main, false)
	pShowWindow.Call(d, 5)
	pSetFocus.Call(g.dName)
}

func (g *gui) syncConnection() {
	usb := send(g.dUSB, bmGetCheck, 0, 0) == 1
	enable(g.dIP, !usb)
	enable(g.dUSBList, usb && len(g.usb) > 0)
}

func (g *gui) closePrinter() {
	enable(g.main, true)
	pDestroyWindow.Call(g.dlg)
	g.dlg = 0
	pSetForeground.Call(g.main)
}

func (g *gui) dlgProc(hwnd, m, w, l uintptr) uintptr {
	switch m {
	case wmCommand:
		id, code := int(w&0xFFFF), int(w>>16)
		switch {
		case id == idCancel:
			g.closePrinter()
		case id == idOK:
			g.printerOK()
		case id == idNetwork || id == idUSB:
			g.syncConnection()
		case id == idName && code == enChange && !g.dIDTouched:
			g.dSettingID = true
			setText(g.dID, suggestID(getText(g.dName), len(g.printers)+1))
			g.dSettingID = false
		case id == idID && code == enChange && !g.dSettingID:
			g.dIDTouched = true
		}
		return 0
	case wmCtlColorStatic:
		pSetBkMode.Call(w, 1)
		if l == g.dError {
			pSetTextColor.Call(w, 0x0000B0) // dark red (BGR)
		}
		b, _, _ := pGetSysColorBrush.Call(colorWindow)
		return b
	case wmClose:
		g.closePrinter()
		return 0
	}
	r, _, _ := pDefWindowProc.Call(hwnd, m, w, l)
	return r
}

func (g *gui) printerOK() {
	in := printerInput{Name: getText(g.dName), ID: getText(g.dID), Width: 80}
	if send(g.dWidth, cbGetCurSel, 0, 0) == 1 {
		in.Width = 58
	}
	if send(g.dUSB, bmGetCheck, 0, 0) == 1 {
		in.USB = true
		if k := int(int32(send(g.dUSBList, cbGetCurSel, 0, 0))); k >= 0 && k < len(g.usb) {
			in.Where = g.usb[k]
		}
	} else {
		in.Where = getText(g.dIP)
	}
	if k := int(int32(send(g.dBackup, cbGetCurSel, 0, 0))); k > 0 && k < len(g.dBackups) {
		in.Backup = g.dBackups[k]
	}
	g.syncRaw()
	p, err := makePrinter(g.raw, in, g.dEditing)
	if err != nil {
		setText(g.dError, strings.ToUpper(err.Error()[:1])+err.Error()[1:]+".")
		return
	}
	editing := g.dEditing
	g.closePrinter()

	var before config.Printer
	if editing == "" {
		g.printers = append(g.printers, guiPrinter{p: p})
	} else {
		i := g.index(editing)
		before = g.printers[i].p
		g.printers[i] = guiPrinter{p: p}
	}
	send(g.list, lbSetCurSel, uintptr(g.index(p.ID)), 0)
	g.refreshList()

	needsTest := editing == "" || p.Kind != before.Kind || p.Address != before.Address || p.PrinterName != before.PrinterName || p.Width != before.Width
	if !g.installed {
		if needsTest {
			g.testPrinter(p.ID, nil)
		}
		return
	}
	// Installed: test first, then save and restart, as "printer set" does.
	what := "added " + p.Describe()
	if editing != "" {
		what = before.Describe() + "  ->  " + p.Describe()
	}
	if !needsTest {
		g.saveInstalledChange(what)
		return
	}
	g.testPrinter(p.ID, func(err error) {
		if err != nil && messageBox(g.main, "The test print failed:\n"+err.Error()+"\n\nSave this change anyway?", "Palate print agent", mbYesNo|mbIconWarn) != idYes {
			// Put the printer back as it was.
			if editing == "" {
				g.printers = g.printers[:len(g.printers)-1]
			} else if i := g.index(p.ID); i >= 0 {
				g.printers[i] = guiPrinter{p: before}
			}
			g.refreshList()
			g.say("  Not saved.")
			return
		}
		g.saveInstalledChange(what)
	})
}

// guiAvailable reports whether this build can show the setup window.
func guiAvailable() bool { return true }
