//go:build windows

package main

import "time"

// sendToSupport makes the report for support in the background (the Edge
// check takes a few seconds), copies it, saves it on the Desktop, and says
// so. g is the window asking.
func sendToSupport(g *gui, cfgPath string) {
	if g.busy {
		return
	}
	g.setBusy(true)
	if g.status != 0 {
		g.say("Making a report for support (it checks Microsoft Edge: a few seconds)...")
	}
	go func() {
		report := makeSupportReport(cfgPath)
		now := time.Now()
		g.later(func() {
			g.setBusy(false)
			copyErr := copyToClipboard(report)
			path, saveErr := saveReport(desktopDir(), report, now)
			var msg string
			switch {
			case copyErr == nil && saveErr == nil:
				msg = "The report for support is ready, and copied.\n\n" +
					"Paste it (Ctrl+V) into WhatsApp or an email to Palate support.\n\n" +
					"It's also saved on the Desktop as:\n" + reportFileName(now)
			case saveErr == nil:
				msg = "The report for support is saved on the Desktop as:\n" + reportFileName(now) +
					"\n\nSend that file to Palate support. (It couldn't be copied: " + copyErr.Error() + ".)"
			case copyErr == nil:
				msg = "The report for support is copied. Paste it (Ctrl+V) into WhatsApp or an email to Palate support." +
					"\n\n(It couldn't be saved on the Desktop: " + saveErr.Error() + ".)"
			default:
				msg = "The report couldn't be copied or saved: " + copyErr.Error() + "; " + saveErr.Error() + "."
			}
			if g.status != 0 {
				if saveErr == nil {
					g.say("  Report saved: " + path)
				}
				g.say("  " + firstLine(msg))
			}
			messageBox(g.main, msg, "Palate print agent", mbOK|mbIconInfo)
		})
	}()
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}

// askYesNo asks a question in a message box with no owner window.
func askYesNo(text string, warn bool) bool {
	icon := uintptr(mbIconInfo)
	if warn {
		icon = mbIconWarn
	}
	return messageBox(0, text, "Palate print agent", mbYesNo|icon) == idYes
}
