//go:build windows

package main

// The "Find printers" dialog of the setup window: it searches this PC's
// network for receipt printers, can make each one print its own address so
// staff can tell them apart, and adds the chosen one through the usual
// printer dialog with its address filled in.

import (
	"context"
	"fmt"
	"unsafe"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/printer/discover"
)

// startSearch searches in the background. then, if given, runs on the
// window's thread when it finishes.
func (g *gui) startSearch(then func()) {
	if g.env.find == nil || g.searching {
		return
	}
	g.searching = true
	g.syncFind()
	go func() {
		found, nets, err := g.env.find(context.Background())
		g.later(func() {
			g.searching, g.searched = false, true
			g.found, g.nets, g.findErr = found, nets, err
			g.syncFind()
			if then != nil {
				then()
			}
		})
	}()
}

// receiptCount counts found receipt printers not yet on this till.
func (g *gui) receiptCount() int {
	g.syncRaw()
	n := 0
	for _, f := range g.found {
		if f.Answers && addedAt(g.raw, f.Addr) == nil {
			n++
		}
	}
	return n
}

// autoSearch runs once when a new till's window opens, and says what it found.
func (g *gui) autoSearch() {
	g.say("Looking for network printers on this PC's network...")
	g.startSearch(func() {
		switch n := g.receiptCount(); {
		case n == 1:
			g.say("  Found 1 receipt printer. Press Find printers... to add it.")
		case n > 1:
			g.say(fmt.Sprintf("  Found %d receipt printers. Press Find printers... to add them.", n))
		default:
			g.say("  No network printers found. Add them by IP address, or check they're on and cabled to this network.")
		}
	})
}

func (g *gui) openFind() {
	if g.fdlg != 0 || g.dlg != 0 || g.busy {
		return
	}
	g.fdlg = g.window("PalateFind", "Find printers", 520, 420, g.main)
	d := g.fdlg
	g.label(d, "Printers on this till's network", 24, 16, 472, 22, g.bold)
	g.fStatus = g.label(d, "", 24, 40, 472, 40, 0)
	g.fList = g.ctl(d, "LISTBOX", "", wsTabStop|wsVScroll|lbsNotify|lbsNoIntegral, wsExClientEdge, idFindList, 24, 84, 472, 200, 0)
	g.label(d, "Not sure which is which? Print address slips: each receipt printer prints its own address, so you can read it off the printer.", 24, 292, 472, 40, 0)
	g.fAgain = g.button(d, "Search again", idFindAgain, 24, 340, 110, false)
	g.fSlips = g.button(d, "Print address slips", idFindSlips, 140, 340, 150, false)
	g.fAdd = g.button(d, "Add selected...", idFindAdd, 24, 378, 140, true)
	g.button(d, "Close", idFindClose, 406, 378, 90, false)
	enable(g.main, false)
	pShowWindow.Call(d, 5)
	if !g.searched && !g.searching {
		g.startSearch(nil)
	}
	g.syncFind()
	pSetFocus.Call(g.fList)
}

// syncFind shows the search's state and results in the dialog, if open.
func (g *gui) syncFind() {
	if g.fdlg == 0 {
		return
	}
	g.syncRaw()
	sel := int(int32(send(g.fList, lbGetCurSel, 0, 0)))
	send(g.fList, lbResetContent, 0, 0)
	for _, f := range g.found {
		send(g.fList, lbAddString, 0, uintptr(unsafe.Pointer(wstr(foundLabel(f, g.raw)))))
	}
	switch {
	case g.searching:
		setText(g.fStatus, "Searching this PC's network for receipt printers (a few seconds)...")
	case g.findErr != nil && len(g.found) == 0:
		setText(g.fStatus, "The search didn't work: "+g.findErr.Error())
	case len(g.found) == 0:
		setText(g.fStatus, "None found on "+networksLabel(g.nets)+". Check the printer is on and cabled to this network, or add it by its IP address.")
	default:
		setText(g.fStatus, fmt.Sprintf("Searched %s. Choose a printer and press Add selected.", networksLabel(g.nets)))
	}
	if sel < 0 || sel >= len(g.found) {
		sel = 0
		for i, f := range g.found {
			if f.Answers && addedAt(g.raw, f.Addr) == nil {
				sel = i
				break
			}
		}
	}
	if len(g.found) > 0 {
		send(g.fList, lbSetCurSel, uintptr(sel), 0)
	}
	g.syncFindButtons()
}

func (g *gui) syncFindButtons() {
	if g.fdlg == 0 {
		return
	}
	work := g.searching || g.identifying
	enable(g.fAgain, !work)
	enable(g.fSlips, !work && g.receiptCountAll() > 0)
	enable(g.fAdd, !work && g.findSelected() >= 0)
}

func (g *gui) receiptCountAll() int {
	n := 0
	for _, f := range g.found {
		if f.Answers {
			n++
		}
	}
	return n
}

func (g *gui) findSelected() int {
	i := int(int32(send(g.fList, lbGetCurSel, 0, 0)))
	if i < 0 || i >= len(g.found) {
		return -1
	}
	return i
}

func (g *gui) closeFind() {
	if g.identifying {
		return // finish the slips first
	}
	enable(g.main, true)
	pDestroyWindow.Call(g.fdlg)
	g.fdlg = 0
	pSetForeground.Call(g.main)
}

// printSlips prints each receipt printer's address on it.
func (g *gui) printSlips() {
	if g.env.identify == nil {
		return
	}
	var addrs []string
	for _, f := range g.found {
		if f.Answers {
			addrs = append(addrs, f.Addr)
		}
	}
	g.identifying = true
	g.syncFindButtons()
	setText(g.fStatus, "Printing each printer's address on it...")
	go func() {
		var failed []string
		for _, a := range addrs {
			if err := g.env.identify(a); err != nil {
				failed = append(failed, err.Error())
			}
		}
		g.later(func() {
			g.identifying = false
			msg := fmt.Sprintf("Printed address slips on %d printer(s). Find each slip, then add that printer under the name the kitchen uses.", len(addrs)-len(failed))
			for _, f := range failed {
				msg += "\r\nCouldn't print on " + f
			}
			g.say(msg)
			if g.fdlg != 0 {
				setText(g.fStatus, msg)
				g.syncFindButtons()
			}
		})
	}()
}

// addFound opens the printer dialog with the chosen printer's address.
func (g *gui) addFound() {
	i := g.findSelected()
	if i < 0 {
		return
	}
	f := g.found[i]
	g.syncRaw()
	if p := addedAt(g.raw, f.Addr); p != nil {
		messageBox(g.fdlg, shortAddr(f.Addr)+" is already added as "+p.Name+".", "Palate print agent", mbOK|mbIconInfo)
		return
	}
	g.closeFind()
	g.prefill = &discover.Found{Addr: f.Addr, Windows: f.Windows}
	g.openPrinter(-1)
}

func (g *gui) findProc(hwnd, m, w, l uintptr) uintptr {
	switch m {
	case wmCommand:
		id, code := int(w&0xFFFF), int(w>>16)
		switch {
		case id == idFindClose:
			g.closeFind()
		case id == idFindAgain:
			g.startSearch(nil)
		case id == idFindSlips:
			g.printSlips()
		case id == idFindAdd || (id == idFindList && code == lbnDblClk):
			if !g.searching && !g.identifying {
				g.addFound()
			}
		case id == idFindList:
			g.syncFindButtons()
		}
		return 0
	case wmCtlColorStatic:
		pSetBkMode.Call(w, 1)
		b, _, _ := pGetSysColorBrush.Call(colorWindow)
		return b
	case wmClose:
		g.closeFind()
		return 0
	}
	r, _, _ := pDefWindowProc.Call(hwnd, m, w, l)
	return r
}
