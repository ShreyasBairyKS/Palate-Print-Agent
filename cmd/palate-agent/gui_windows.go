//go:build windows

package main

// The print agent's window: one small native Windows window, drawn with
// Windows' own controls and theme, for everything a restaurant does with the
// agent. Before it's installed it walks through setup; afterwards the same
// window shows whether the printers are working (refreshed every two
// seconds), and changes them, pairs, updates, sends a report to support and
// removes the agent.
//
// Opened on an installed till without administrator rights (the usual
// double-click), it shows everything and can test-print, but its buttons that
// change something carry Windows' shield: pressing one asks Windows for
// permission, and the window comes back in the same place, with permission,
// already doing what was pressed.
//
// It adds no dependency, opens no port and starts no other program; every
// step it takes is the same code the console setup and the "printer"
// commands use (setupflow.go).

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/config"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/job"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/pair"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/printer/discover"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/service"
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
	pOpenClipboard     = user32.NewProc("OpenClipboard")
	pEmptyClipboard    = user32.NewProc("EmptyClipboard")
	pSetClipboardData  = user32.NewProc("SetClipboardData")
	pCloseClipboard    = user32.NewProc("CloseClipboard")
	pGlobalAlloc       = kernel32.NewProc("GlobalAlloc")
	pGlobalLock        = kernel32.NewProc("GlobalLock")
	pGlobalUnlock      = kernel32.NewProc("GlobalUnlock")
	pGlobalFree        = kernel32.NewProc("GlobalFree")
	pMoveMemory        = kernel32.NewProc("RtlMoveMemory")
	pSetTimer          = user32.NewProc("SetTimer")
	pKillTimer         = user32.NewProc("KillTimer")
	pFlashWindowEx     = user32.NewProc("FlashWindowEx")
	pInvalidateRect    = user32.NewProc("InvalidateRect")
	pGetWindowRect     = user32.NewProc("GetWindowRect")
	pSetWindowPos      = user32.NewProc("SetWindowPos")
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
	cbnSelChange                             = 1
	mbOK, mbYesNo, mbIconErr, mbIconWarn     = 0x0, 0x4, 0x10, 0x30
	mbIconInfo, idYes, mbDefButton2          = 0x40, 6, 0x100
	colorWindow                              = 5
	wmUser                                   = wmApp + 1
	wmTimer, bcmSetShield                    = 0x113, 0x160C

	refreshTimer = 1        // reads the agent's status again
	handOffTimer = 2        // closes this copy once the one with permission is up
	refreshEvery = 2000     // ms
	handOffAfter = 2000     // ms
	rgbRed       = 0x0000B4 // COLORREF is 0x00BBGGRR
	rgbGreen     = 0x00701A
	rgbDarkRed   = 0x0000B0
	windowTitle  = "Palate print agent"
)

type flashInfo struct {
	cbSize    uint32
	hwnd      uintptr
	dwFlags   uint32
	uCount    uint32
	dwTimeout uint32
}

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
	idDots
	idServerChange
	idSupport
	idUninstall
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

// notify shows a message box with no owner window.
func notify(text string, bad bool) {
	flags := uintptr(mbOK | mbIconInfo)
	if bad {
		flags = mbOK | mbIconErr
	}
	messageBox(0, text, "Palate print agent", flags)
}

// copyToClipboard puts text on the clipboard, for pasting into WhatsApp or
// an email.
func copyToClipboard(text string) error {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n")
	u, err := windows.UTF16FromString(text)
	if err != nil {
		return err
	}
	opened := false
	for i := 0; i < 10 && !opened; i++ { // another program may hold it for a moment
		r, _, _ := pOpenClipboard.Call(0)
		opened = r != 0
		if !opened {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if !opened {
		return errors.New("the clipboard is busy")
	}
	defer pCloseClipboard.Call()
	pEmptyClipboard.Call()
	size := uintptr(len(u) * 2)
	h, _, _ := pGlobalAlloc.Call(0x2, size) // GMEM_MOVEABLE
	if h == 0 {
		return errors.New("out of memory for the clipboard")
	}
	p, _, _ := pGlobalLock.Call(h)
	if p == 0 {
		pGlobalFree.Call(h)
		return errors.New("couldn't use the clipboard")
	}
	pMoveMemory.Call(p, uintptr(unsafe.Pointer(&u[0])), size)
	pGlobalUnlock.Call(h)
	if r, _, err := pSetClipboardData.Call(13, h); r == 0 { // CF_UNICODETEXT
		pGlobalFree.Call(h)
		return fmt.Errorf("couldn't copy: %v", err)
	}
	return nil
}

// ---------------------------------------------------------------------------

type guiPrinter struct {
	p    config.Printer
	test string // "", "testing", "ok", or why it failed
}

type gui struct {
	env       *setupEnv
	opts      appOptions
	readOnly  bool // installed, and opened without administrator rights: changes ask Windows first
	installed bool // the service exists on this PC
	paired    bool
	state     string // service state, when installed
	till      string // the till's name, from the status file
	raw       *config.Config
	printers  []guiPrinter
	usb       []string
	busy      bool
	handedOff bool // a copy with administrator rights is taking over

	dpi             int
	font, bold, big uintptr
	title           uintptr
	hinst           uintptr

	main, server, serverChange, list, add, edit, remove, test, find, code, codeLabel, install, status, subtitle uintptr
	support, uninstall, headline, detail, advice, codeHint                                                      uintptr

	// The last status shown, to flash the taskbar when something new breaks.
	bad  bool
	down map[string]bool

	// Finding network printers, and the Find printers dialog while it's open.
	searching, searched, identifying bool
	found                            []discover.Found
	nets                             []discover.Network
	findErr                          error
	prefill                          *discover.Found // a found printer the printer dialog opens with
	fdlg, fStatus, fList             uintptr
	fAgain, fSlips, fAdd             uintptr

	// The printer dialog, while it's open.
	dlg, dName, dID, dNet, dIP, dUSB, dUSBList, dWidth, dDots, dBackup, dError uintptr
	dEditing                                                                   string // id of the printer being edited, or ""
	dIDTouched, dSettingID                                                     bool
	dBackups                                                                   []string

	// What the running agent last said about each printer, once installed.
	live map[string]status.Printer

	mu    sync.Mutex
	queue []func()
}

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

// runGUI shows the print agent's window until it is closed.
func runGUI(e *setupEnv, ownConsole bool, o appOptions) error {
	// A window belongs to the thread that made it, and only that thread gets
	// its messages: stay on one.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if ownConsole {
		// Double-clicked: hide the black console window behind us.
		if h, _, _ := pGetConsoleWindow.Call(); h != 0 {
			pShowWindow.Call(h, 0)
		}
		pFreeConsole.Call()
	}

	g := &gui{env: e, opts: o, dpi: 96, down: map[string]bool{}}
	g.initUI()
	g.load()
	if err := g.register(map[string]func(hwnd, m, w, l uintptr) uintptr{
		"PalateSetup": g.mainProc, "PalatePrinter": g.dlgProc, "PalateFind": g.findProc,
	}); err != nil {
		return fmt.Errorf("window: %v", err)
	}

	g.main = g.window("PalateSetup", windowTitle, 600, 632, 0)
	if g.main == 0 {
		return errors.New("window: couldn't create the window")
	}
	// Reopened with permission: in the same place as the copy it replaces.
	var x, y int32
	if _, err := fmt.Sscanf(o.at, "%d,%d", &x, &y); err == nil {
		pSetWindowPos.Call(g.main, 0, uintptr(x), uintptr(y), 0, 0, 0x1|0x4) // SWP_NOSIZE|SWP_NOZORDER
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
	g.big = g.makeFont(13, 600)
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
	g.raw = &config.Config{}
	if g.installed && !service.IsElevated() {
		// The settings need administrator rights: the printers, address and
		// pairing come from the status file the agent writes (refreshStatus).
		g.readOnly, g.paired = true, true
		return
	}
	all, _ := g.env.usbList()
	g.usb = realPrinters(all)
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
	g.label(m, "Palate print agent", 24, 14, 552, 34, g.title)
	g.subtitle = g.label(m, g.subtitleText(), 24, 50, 552, 20, 0)

	// How it is: whether the printers work, or what's left to set up.
	g.headline = g.label(m, "Set up this till", 24, 78, 552, 30, g.big)
	g.detail = g.label(m, "Add this till's printers (each prints a test slip), type the pairing code from the Palate portal, then press Install and start.", 24, 110, 552, 38, 0)

	g.label(m, "Printers", 24, 152, 552, 20, g.bold)
	g.list = g.ctl(m, "LISTBOX", "", wsTabStop|wsVScroll|lbsNotify|lbsNoIntegral, wsExClientEdge, idPrinters, 24, 174, 552, 132, 0)
	g.add = g.button(m, "Add printer...", idAdd, 24, 314, 124, false)
	g.edit = g.button(m, "Edit...", idEdit, 154, 314, 84, false)
	g.remove = g.button(m, "Remove", idRemove, 244, 314, 90, false)
	g.test = g.button(m, "Test print", idTest, 340, 314, 96, false)
	g.find = g.button(m, "Find printers...", idFind, 442, 314, 134, false)
	g.advice = g.label(m, "", 24, 352, 552, 40, 0)

	g.label(m, "Palate's server", 24, 404, 120, 20, g.bold)
	g.server = g.editBox(m, idServer, 150, 400, 314, "https://", esReadOnly)
	g.serverChange = g.button(m, "Change...", idServerChange, 472, 398, 104, false)
	switch {
	case g.raw.Server != "":
		setText(g.server, g.raw.Server)
	case g.readOnly:
		// An agent older than this one doesn't report its address.
		setText(g.server, "(press Change... to see it)")
	default:
		setText(g.server, config.DefaultServer)
	}

	g.codeLabel = g.label(m, "Pairing code", 24, 444, 120, 20, g.bold)
	g.code = g.editBox(m, idCode, 150, 440, 160, "K7Q2-M9XD", esUppercase)
	g.codeHint = g.label(m, "from the Palate portal (\"Add a till\")", 318, 444, 258, 20, 0)

	g.install = g.button(m, "Install and start", idInstall, 24, 484, 200, true)
	g.support = g.button(m, "Send to support...", idSupport, 270, 484, 150, false)
	g.uninstall = g.button(m, "Remove from this PC...", idUninstall, 426, 484, 150, false)
	if !g.installed {
		pShowWindow.Call(g.uninstall, 0)
	}
	g.status = g.ctl(m, "EDIT", "", wsVScroll|esMultiline|esReadOnly|esAutoVScroll, wsExClientEdge, idStatus, 24, 528, 552, 88, 0)

	if g.readOnly {
		// Windows' shield on everything that asks it for permission first.
		for _, b := range []uintptr{g.add, g.edit, g.remove, g.find, g.serverChange, g.uninstall} {
			send(b, bcmSetShield, 0, 1)
		}
		g.say("Buttons with a shield ask Windows for permission first; this window then reopens and carries on.")
	}
	switch {
	case g.installed:
		g.say("Changes to printers are test-printed, saved, and applied by restarting the service.")
	default:
		if _, err := g.env.edgeFind(); err != nil {
			g.say("Note: Microsoft Edge wasn't found, so customer bills can't print until it's installed.")
		}
		if len(g.usb) == 0 {
			g.say("No USB receipt printers found.")
		}
	}
	if g.installed {
		g.watchLive()
		if c, ok := checkForUpdate(); ok && g.opts.then != "update" {
			g.later(func() { g.offerUpdate(c) })
		}
	}
	g.refreshList()
	g.syncPrimary()
	if !g.installed && len(g.printers) == 0 {
		g.autoSearch()
	}
	if g.opts.then != "" {
		g.later(g.carryOn)
	}
}

// subtitleText is the line under the title: the till, and this program's
// version and build date.
func (g *gui) subtitleText() string {
	v := versionLabel()
	if g.till != "" {
		return "Till " + g.till + "   ·   " + v
	}
	return strings.ToUpper(v[:1]) + v[1:]
}

// primary is what the main button does now.
func (g *gui) primary() string {
	switch {
	case !g.installed:
		return "Install and start"
	case !g.paired:
		return "Pair and start"
	case g.serverChanged():
		return "Pair with new server"
	case g.state != "" && g.state != "running" && g.state != "starting" && g.state != "stopping":
		return "Start printing"
	}
	return "Close"
}

// syncPrimary labels the main button, and shows the pairing code box when
// the button needs a code.
func (g *gui) syncPrimary() {
	label := g.primary()
	if getText(g.install) != label {
		setText(g.install, label)
	}
	show := uintptr(0)
	if label == "Install and start" || label == "Pair and start" || label == "Pair with new server" {
		show = 5
	}
	for _, h := range []uintptr{g.codeLabel, g.code, g.codeHint} {
		pShowWindow.Call(h, show)
	}
	send(g.install, bcmSetShield, 0, boolArg(g.readOnly && label != "Close"))
}

// refreshStatus reads what the running agent last wrote, and shows it: the
// headline (green when all's well, red when not), the connection, the
// printers' states and what to do about a problem. A printer that has just
// gone down, or a new problem, flashes the window in the taskbar.
func (g *gui) refreshStatus() {
	if !g.installed {
		return
	}
	svc := "unknown"
	switch info, err := g.env.status(); {
	case err == nil:
		svc, g.state = info.State, info.State
	case errors.Is(err, service.ErrNotInstalled):
		svc = ""
	}
	f, err := status.Read(status.PathFor(g.env.cfgPath))
	v := describeStatus(f, err, svc, time.Now())
	if err == nil && f.Till != g.till {
		g.till = f.Till
		setText(g.subtitle, g.subtitleText())
	}
	setText(g.headline, v.Headline)
	setText(g.detail, v.Detail)
	setText(g.advice, v.Advice)
	title := windowTitle
	if v.Bad {
		title += " — " + v.Headline
	}
	setText(g.main, title)

	listChanged := g.readLive()
	if g.readOnly && err == nil && g.fromStatus(f) {
		listChanged = true
	}
	if listChanged {
		g.refreshList()
	}
	g.syncPrimary()

	alarm := v.Bad && !g.bad
	down := map[string]bool{}
	for _, n := range v.Down {
		down[n] = true
		if !g.down[n] {
			alarm = true
		}
	}
	g.bad, g.down = v.Bad, down
	if alarm {
		fi := flashInfo{hwnd: g.main, dwFlags: 0x3 | 0xC} // caption and taskbar, until the window is brought forward
		fi.cbSize = uint32(unsafe.Sizeof(fi))
		pFlashWindowEx.Call(uintptr(unsafe.Pointer(&fi)))
	}
	// Repaint the headline and advice in their new colours.
	pInvalidateRect.Call(g.headline, 0, 1)
	pInvalidateRect.Call(g.advice, 0, 1)
}

// fromStatus takes the printers, the server address and whether the till is
// paired from the status file, for a window without permission to read the
// settings. It reports whether the printers changed.
func (g *gui) fromStatus(f status.File) bool {
	if f.ServerURL != "" && f.ServerURL != g.raw.Server {
		g.raw.Server = f.ServerURL
		setText(g.server, f.ServerURL)
	}
	g.paired = !strings.Contains(f.Stopped, "pair") && !strings.Contains(f.Stopped, "unauthorized")
	var ps []guiPrinter
	for _, p := range f.Printers {
		cp := config.Printer{ID: p.ID, Name: p.Name, Kind: p.Kind, Width: p.Width, Dots: p.Dots, Backup: p.Backup}
		if p.Kind == "usb" {
			cp.PrinterName = p.Where
		} else {
			cp.Address = p.Where
		}
		gp := guiPrinter{p: cp}
		if i := g.index(p.ID); i >= 0 {
			gp.test = g.printers[i].test
		}
		ps = append(ps, gp)
	}
	changed := len(ps) != len(g.printers)
	for i := range ps {
		if !changed && ps[i] != g.printers[i] {
			changed = true
		}
	}
	g.printers = ps
	g.syncRaw()
	return changed
}

// elevate asks Windows for permission to change things, by opening this
// window again with administrator rights, in the same place, set to do
// action first (carryOn). This copy closes once that one is up. Turned down,
// nothing changes.
func (g *gui) elevate(action, code string) {
	if g.busy || g.handedOff {
		return
	}
	args := []string{"setup", "-gui", "-config", g.env.cfgPath, "-then", action}
	if code != "" {
		args = append(args, "-code", code)
	}
	var r rect
	pGetWindowRect.Call(g.main, uintptr(unsafe.Pointer(&r)))
	args = append(args, "-at", fmt.Sprintf("%d,%d", r.left, r.top))
	if err := relaunchElevated(args); err != nil {
		if errors.Is(err, windows.ERROR_CANCELLED) {
			g.say("Windows didn't give permission, so nothing changed.")
			return
		}
		messageBox(g.main, "Windows couldn't give this window permission to change things: "+err.Error()+
			"\n\nRight-click palate-agent.exe and choose \"Run as administrator\".", windowTitle, mbOK|mbIconErr)
		return
	}
	g.handedOff = true
	g.say("Opening with permission...")
	pSetTimer.Call(g.main, handOffTimer, handOffAfter, 0)
}

// carryOn does what was pressed before this window reopened with
// administrator rights.
func (g *gui) carryOn() {
	action, id, _ := strings.Cut(g.opts.then, ":")
	pick := func() int {
		i := g.index(id)
		if i >= 0 {
			send(g.list, lbSetCurSel, uintptr(i), 0)
			g.setBusy(g.busy)
		}
		return i
	}
	switch action {
	case "add":
		g.openPrinter(-1)
	case "find":
		g.openFind()
	case "edit":
		if i := pick(); i >= 0 {
			g.openPrinter(i)
		}
	case "remove":
		if pick() >= 0 {
			g.removePrinter()
		}
	case "server":
		g.changeServer()
	case "pair", "start":
		if g.opts.code != "" {
			setText(g.code, g.opts.code)
		}
		switch label := g.primary(); {
		case label == "Close":
			g.say("The print agent is already running.")
		case label == "Start printing" || g.opts.code != "":
			g.installClicked()
		default:
			g.say("Type the pairing code from the Palate portal (\"Add a till\"), then press " + label + ".")
			pSetFocus.Call(g.code)
		}
	case "uninstall":
		g.removeClicked()
	case "update":
		if c, ok := checkForUpdate(); ok {
			g.runUpdate(c)
		}
	}
}

// changeServer lets the address be typed. It's read-only until asked, so
// nobody changes it by accident.
func (g *gui) changeServer() {
	send(g.server, emSetReadOnly, 0, 0)
	pSetFocus.Call(g.server)
	send(g.server, emSetSel, 0, ^uintptr(0))
	enable(g.serverChange, false)
}

// typedServer is the address in the box, tidied (https:// and
// /api/print-agent added when they're missing), and shows it tidied.
func (g *gui) typedServer() string {
	s := config.NormalizeServer(getText(g.server))
	if s != getText(g.server) {
		setText(g.server, s)
	}
	return s
}

// offerUpdate asks to replace the installed program with this one.
func (g *gui) offerUpdate(c buildCompare) {
	icon := uintptr(mbIconInfo)
	if c.Older {
		icon = mbIconWarn
	}
	if g.busy || messageBox(g.main, updateOffer(c), "Palate print agent", mbYesNo|icon) != idYes {
		return
	}
	if g.readOnly {
		g.elevate("update", "")
		return
	}
	g.runUpdate(c)
}

// runUpdate replaces the installed program with this one, in the background.
func (g *gui) runUpdate(c buildCompare) {
	g.setBusy(true)
	g.say("Updating the installed program...")
	go func() {
		err := updateProgram(g.env.cfgPath, func(s string) { g.later(func() { g.say("  " + s) }) })
		g.later(func() {
			g.setBusy(false)
			if err != nil {
				pe := explain(err)
				g.say("Problem: " + pe.String())
				messageBox(g.main, "The update didn't finish.\n\n"+pe.Box(), "Palate print agent", mbOK|mbIconErr)
				return
			}
			g.refreshStatus()
			messageBox(g.main, "Updated to "+versionLabel()+".\n\nPrinters, settings and pairing are as they were.", "Palate print agent", mbOK|mbIconInfo)
		})
	}()
}

// removeClicked removes the agent from this PC, after asking twice: once to
// be sure, and once whether to keep the settings for a reinstall.
func (g *gui) removeClicked() {
	if g.busy {
		return
	}
	if g.readOnly {
		g.elevate("uninstall", "")
		return
	}
	if messageBox(g.main, "Remove the Palate print agent from this PC?\n\nTickets and bills stop printing on this till until it's installed again.",
		"Palate print agent", mbYesNo|mbIconWarn) != idYes {
		return
	}
	dataDir := filepath.Dir(g.env.cfgPath)
	deleteData := messageBox(g.main, "Also delete this till's printers, settings and pairing?\n\n"+
		"No: keep them in "+dataDir+", so installing again later is quick (recommended).\n\n"+
		"Yes: delete everything, for example before the PC is given away.",
		"Palate print agent", mbYesNo|mbIconWarn|mbDefButton2) == idYes
	g.setBusy(true)
	g.say("Removing the print agent...")
	go func() {
		kept, err := removeAgent(g.env.cfgPath, deleteData)
		g.later(func() {
			g.setBusy(false)
			if err != nil {
				pe := explain(err)
				g.say("Problem: " + pe.String())
				messageBox(g.main, pe.Box(), "Palate print agent", mbOK|mbIconErr)
				if _, serr := g.env.status(); serr == nil {
					return // still installed
				}
			}
			msg := "The Palate print agent is removed from this PC."
			if kept != "" {
				msg += "\n\nIts printers, settings and pairing are kept in " + kept + ". Double-click palate-agent.exe to install it again."
			}
			msg += "\n\nIf this till won't be used again, remove it in the Palate portal too."
			messageBox(g.main, msg, "Palate print agent", mbOK|mbIconInfo)
			pDestroyWindow.Call(g.main)
		})
	}()
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
		line := fmt.Sprintf("%s   (%s)   ·   %s   ·   %s%s%s", p.Name, p.ID, where, p.Paper(), backup, mark)
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
	enable(g.server, !b)
	enable(g.support, !b)
	enable(g.uninstall, !b)
}

// serverChanged reports whether the address box differs from the saved one.
func (g *gui) serverChanged() bool {
	if g.readOnly {
		return false // it can't be typed in until the window has permission
	}
	return !sameServer(config.NormalizeServer(getText(g.server)), g.raw.Server)
}

// serverEdited follows the address box on an installed, paired till: a new
// address needs a pairing code from that server, so the code box appears
// and the button says what it will do.
func (g *gui) serverEdited() {
	if g.install != 0 {
		g.syncPrimary()
	}
}

// selectedID is the selected printer's id, or "".
func (g *gui) selectedID() string {
	if i := g.selected(); i >= 0 {
		return g.printers[i].p.ID
	}
	return ""
}

func (g *gui) mainProc(hwnd, m, w, l uintptr) uintptr {
	switch m {
	case wmCommand:
		id, code := int(w&0xFFFF), int(w>>16)
		switch {
		case id == idAdd && g.readOnly:
			g.elevate("add", "")
		case id == idAdd:
			g.openPrinter(-1)
		case id == idFind && g.readOnly:
			g.elevate("find", "")
		case id == idFind:
			g.openFind()
		case id == idEdit || (id == idPrinters && code == lbnDblClk):
			if i := g.selected(); i >= 0 && !g.busy {
				if g.readOnly {
					g.elevate("edit:"+g.selectedID(), "")
				} else {
					g.openPrinter(i)
				}
			}
		case id == idRemove && g.readOnly:
			if p := g.selectedID(); p != "" {
				g.elevate("remove:"+p, "")
			}
		case id == idRemove:
			g.removePrinter()
		case id == idTest:
			if i := g.selected(); i >= 0 {
				g.testPrinter(g.printers[i].p.ID, nil)
			}
		case id == idInstall:
			g.installClicked()
		case id == idServerChange && g.readOnly:
			g.elevate("server", "")
		case id == idServerChange:
			g.changeServer()
		case id == idSupport:
			sendToSupport(g, g.env.cfgPath)
		case id == idUninstall:
			g.removeClicked()
		case id == idServer && code == enChange:
			g.serverEdited()
		case id == idPrinters:
			g.setBusy(g.busy)
		}
		return 0
	case wmCtlColorStatic:
		pSetBkMode.Call(w, 1)
		switch {
		case l == g.headline && g.installed && g.bad:
			pSetTextColor.Call(w, rgbRed)
		case l == g.headline && g.installed:
			pSetTextColor.Call(w, rgbGreen)
		case l == g.advice:
			pSetTextColor.Call(w, rgbDarkRed)
		}
		b, _, _ := pGetSysColorBrush.Call(colorWindow)
		return b
	case wmUser:
		g.drain()
		return 0
	case wmTimer:
		if w == handOffTimer {
			pDestroyWindow.Call(hwnd)
		} else {
			g.refreshStatus()
		}
		return 0
	case wmClose:
		if g.busy {
			messageBox(hwnd, "Still working. Wait for it to finish, then close the window.", "Palate print agent", mbOK|mbIconInfo)
			return 0
		}
		pDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		pKillTimer.Call(hwnd, refreshTimer)
		pKillTimer.Call(hwnd, handOffTimer)
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
					g.say("  " + p.Name + ": the test print failed. " + explain(err).String())
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
	label := g.primary()
	switch {
	case label == "Close":
		pDestroyWindow.Call(g.main)
		return
	case g.readOnly && label == "Start printing":
		g.elevate("start", "")
		return
	case g.readOnly:
		g.elevate("pair", strings.ToUpper(strings.TrimSpace(getText(g.code))))
		return
	case label == "Start printing":
		g.startAgent()
		return
	case g.installed:
		g.pairAndStart()
		return
	}
	server := g.typedServer()
	if err := config.CheckServer(server); err != nil {
		messageBox(g.main, "Palate's server address: "+err.Error()+".", "Palate print agent", mbOK|mbIconWarn)
		g.changeServer()
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
				pe := explain(err)
				g.say("Problem: " + pe.String())
				messageBox(g.main, pe.Box(), "Palate print agent", mbOK|mbIconErr)
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

// startAgent starts the installed, paired agent.
func (g *gui) startAgent() {
	g.setBusy(true)
	g.say("Starting the print agent...")
	go func() {
		err := g.env.start()
		g.later(func() {
			g.setBusy(false)
			if err != nil {
				pe := explain(err)
				g.say("Problem: " + pe.String())
				messageBox(g.main, "The print agent didn't start.\n\n"+pe.Box(), windowTitle, mbOK|mbIconErr)
			} else {
				g.say("  Running. Tickets and bills print as soon as Palate sends them.")
			}
			g.refreshStatus()
		})
	}()
}

// watchLive shows the running agent's status, read again every two seconds.
func (g *gui) watchLive() {
	g.refreshStatus()
	pSetTimer.Call(g.main, refreshTimer, refreshEvery, 0)
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
	first := !g.installed
	g.installed, g.paired, g.state = true, paired, state
	pShowWindow.Call(g.uninstall, 5)
	g.syncPrimary()
	if first {
		g.watchLive()
	} else {
		g.refreshStatus()
	}
}

func (g *gui) pairAndStart() {
	code := strings.ToUpper(strings.TrimSpace(getText(g.code)))
	if code == "" {
		messageBox(g.main, "Type the pairing code from the Palate portal (\"Add a till\").", "Palate print agent", mbOK|mbIconInfo)
		pSetFocus.Call(g.code)
		return
	}
	server := g.typedServer()
	if err := config.CheckServer(server); err != nil {
		messageBox(g.main, "Palate's server address: "+err.Error()+".", "Palate print agent", mbOK|mbIconWarn)
		g.changeServer()
		return
	}
	moving := g.serverChanged()
	if moving && g.paired && messageBox(g.main, "Move this till from\n"+g.raw.Server+"\nto\n"+server+"?\n\n"+
		"It pairs with the new server first. If that fails, nothing changes and the till keeps printing from the old one.",
		"Palate print agent", mbYesNo|mbIconWarn) != idYes {
		return
	}
	g.setBusy(true)
	g.say("Pairing with Palate at " + server + "...")
	go func() {
		dev, moved, err := pairServer(g.env, server, code)
		msg := ""
		if err == nil {
			if moved {
				g.later(func() { g.say("Paired as " + dev + ". Saved the new address.") })
			} else {
				g.later(func() { g.say("Paired as " + dev + ".") })
			}
			// A running agent still holds the old credential (and address):
			// restart it. A stopped one is started.
			if info, serr := g.env.status(); serr == nil && info.State == "running" {
				g.later(func() { g.say("Restarting the print agent...") })
				msg = restartService(g.env.cfgPath)
			} else {
				g.later(func() { g.say("Starting the print agent...") })
				err = g.env.start()
			}
		}
		g.later(func() {
			g.setBusy(false)
			if err != nil {
				pe := explain(err)
				g.say("Problem: " + pe.String())
				messageBox(g.main, pe.Box(), "Palate print agent", mbOK|mbIconErr)
				return
			}
			if moved {
				g.raw.Server = server
			}
			if msg != "" {
				g.say("  " + msg)
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
	g.dlg = g.window("PalatePrinter", title, 440, 416, g.main)
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

	g.label(d, "Paper", 24, 234, 150, 20, 0)
	g.dWidth = g.ctl(d, "COMBOBOX", "", wsTabStop|wsVScroll|cbsDropDownList, 0, idWidth, 180, 230, 236, 200, 0)
	for _, ps := range paperSizes {
		send(g.dWidth, cbAddString, 0, uintptr(unsafe.Pointer(wstr(ps.label))))
	}
	g.label(d, "Dots across", 24, 270, 150, 20, 0)
	g.dDots = g.editBox(d, idDots, 180, 266, 80, "e.g. 640", 0)
	g.label(d, "from its spec sheet", 268, 270, 148, 20, 0)

	g.label(d, "Backup when it's down", 24, 306, 150, 20, 0)
	g.dBackup = g.ctl(d, "COMBOBOX", "", wsTabStop|wsVScroll|cbsDropDownList, 0, idBackup, 180, 302, 236, 160, 0)
	g.dBackups = []string{""}
	send(g.dBackup, cbAddString, 0, uintptr(unsafe.Pointer(wstr("None"))))
	for _, gp := range g.printers {
		if gp.p.ID != g.dEditing {
			g.dBackups = append(g.dBackups, gp.p.ID)
			send(g.dBackup, cbAddString, 0, uintptr(unsafe.Pointer(wstr(gp.p.Name+"  ("+gp.p.ID+")"))))
		}
	}

	g.dError = g.label(d, "", 24, 338, 392, 36, 0)
	g.button(d, "Cancel", idCancel, 230, 374, 90, false)
	g.button(d, ok, idOK, 326, 374, 90, true)

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
	if p.Width == 0 {
		p.Width = 80
	}
	send(g.dWidth, cbSetCurSel, uintptr(paperIndex(p)), 0)
	setText(g.dDots, fmt.Sprint(p.PrintDots()))
	g.syncPaper()
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

// paperSizes are the choices in the printer dialog's Paper list. The last
// one has no dots: the person types them.
var paperSizes = []struct {
	label       string
	width, dots int
}{
	{"80 mm (576 dots, most printers)", 80, 576},
	{"58 mm (384 dots)", 58, 384},
	{"80 mm, 512 dots (Epson TM-T88)", 80, 512},
	{"112 mm (832 dots)", 80, 832},
	{"Other: type the dots across", 0, 0},
}

// paperIndex is the Paper list entry for a printer.
func paperIndex(p config.Printer) int {
	for i, ps := range paperSizes {
		if ps.dots != 0 && ps.width == p.Width && ps.dots == p.PrintDots() {
			return i
		}
	}
	return len(paperSizes) - 1
}

// syncPaper shows the chosen paper's dots, and lets them be typed only for
// "Other".
func (g *gui) syncPaper() {
	k := int(int32(send(g.dWidth, cbGetCurSel, 0, 0)))
	custom := k == len(paperSizes)-1
	if !custom && k >= 0 && k < len(paperSizes) {
		setText(g.dDots, fmt.Sprint(paperSizes[k].dots))
	}
	send(g.dDots, emSetReadOnly, boolArg(!custom), 0)
}

func boolArg(b bool) uintptr {
	if b {
		return 1
	}
	return 0
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
		case id == idWidth && code == cbnSelChange:
			g.syncPaper()
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
	k := int(int32(send(g.dWidth, cbGetCurSel, 0, 0)))
	if k < 0 || k >= len(paperSizes) {
		k = 0
	}
	if ps := paperSizes[k]; ps.dots != 0 {
		in.Width, in.Dots = ps.width, ps.dots
	} else {
		n, err := strconv.Atoi(strings.TrimSpace(getText(g.dDots)))
		if err != nil || !job.ValidDots(n) {
			setText(g.dError, fmt.Sprintf("Type how many dots across it prints: a multiple of 8 from %d to %d.", job.MinDots, job.MaxDots))
			pSetFocus.Call(g.dDots)
			return
		}
		in.Width, in.Dots = int(job.ClassFor(n)), n
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

	needsTest := editing == "" || p.Kind != before.Kind || p.Address != before.Address || p.PrinterName != before.PrinterName || p.PrintDots() != before.PrintDots()
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
		if err != nil && messageBox(g.main, "The test print failed.\n\n"+explain(err).Box()+"\n\nSave this change anyway?", "Palate print agent", mbYesNo|mbIconWarn) != idYes {
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
