package main

// The report for support: one button in the status window and the setup
// window gathers what support needs to help with a till (the version,
// Windows, Microsoft Edge and whether it draws bills at full size, the
// service, the connection, the printers, and the recent log), copies it and
// saves it on the Desktop, ready to paste into WhatsApp or an email.
//
// It never includes the pairing credential, and it reads nothing it isn't
// allowed to: without administrator rights the config and the log are left
// out, and the report says so.

import (
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/config"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/htmlrender"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/service"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/status"
)

// reportEnv is what the report reads, so a test can supply it.
type reportEnv struct {
	now       time.Time
	cfgPath   string
	svc       func() (service.Info, error)
	edgeFind  func() (string, error)
	edgeCheck func(edge string) (int, error) // dots across a 60 mm line, as drawn
	self      string                         // this program's path
	installed func() string                  // the installed program's label, or ""
	windows   string                         // Windows' version
}

// edgeCheckMM is the line the Edge check draws, and edgeCheckDots how wide it
// should come out on a 203 dpi printer.
const (
	edgeCheckMM   = 60
	edgeCheckDots = 480
)

// supportReport writes the report.
func supportReport(e reportEnv) string {
	var b strings.Builder
	line := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	host, _ := os.Hostname()

	line("Palate print agent: report for support")
	line("Made:          %s on %s", e.now.Local().Format("2 Jan 2006, 15:04"), host)
	line("Agent:         %s", versionLabel())
	line("This file:     %s", e.self)
	if e.installed != nil {
		if l := e.installed(); l != "" {
			line("Installed:     %s", l)
		}
	}
	line("%-15s%s", osName+":", e.windows)

	svcState := ""
	if info, err := e.svc(); err == nil {
		svcState = info.State
		line("Service:       %s (%s: %v, account %s)", info.State, startsWith, info.AutoStart, info.Account)
		if info.LastExitErr != "" {
			line("               %s", info.LastExitErr)
		}
	} else if errors.Is(err, service.ErrNotInstalled) {
		line("Service:       not installed")
	} else {
		svcState = "unknown"
		line("Service:       couldn't ask %s (%v)", osName, err)
	}

	// Microsoft Edge draws the bills: its version, and whether it draws them
	// at full size here (the half-size problem shows up as about 227 dots).
	if edge, err := e.edgeFind(); err != nil {
		line("%-15snot found (%v). Bills can't print.", browserShort+":", err)
	} else {
		line("%-15s%s (%s)", browserShort+":", edgeVersion(edge), edge)
		if e.edgeCheck != nil {
			dots, err := e.edgeCheck(edge)
			switch {
			case err != nil:
				line("%-15sfailed: %s", browserShort+" check:", explain(err).String())
			case dots >= edgeCheckDots-8 && dots <= edgeCheckDots+8:
				line("%-15sOK. A %d mm line drew %d dots wide (%d expected).", browserShort+" check:", edgeCheckMM, dots, edgeCheckDots)
			default:
				line("%-15sPROBLEM. A %d mm line drew %d dots wide; %d expected, so bills print at the wrong size.", browserShort+" check:", edgeCheckMM, dots, edgeCheckDots)
			}
		}
	}

	// What the running agent says.
	line("")
	f, err := status.Read(status.PathFor(e.cfgPath))
	v := describeStatus(f, err, svcState, e.now)
	line("Status:        %s", v.Headline)
	if v.Detail != "" {
		line("               %s", v.Detail)
	}
	if err == nil {
		line("Till:          %s   (status written %s, agent version %s)", f.Till, ago(f.UpdatedAt, e.now), f.AgentVersion)
		conn := "connected"
		if !f.Server.Connected {
			conn = "NOT connected since " + clock(f.Server.Since, e.now)
		}
		if !f.Server.LastOK.IsZero() {
			conn += ", last worked " + ago(f.Server.LastOK, e.now)
		}
		line("Palate:        %s", conn)
		if f.Server.Error != "" {
			line("               %s", explainText(f.Server.Error).String())
			line("               details: %s", f.Server.Error)
		}
		line("Waiting:       %d ticket(s)", f.QueueDepth)
		line("Printers:")
		for _, p := range f.Printers {
			s := fmt.Sprintf("  %s (%s) at %s, %d mm: %s since %s", p.Name, p.ID, p.Where, p.Width, p.State, clock(p.Since, e.now))
			if p.LastError != "" {
				s += ", error code: " + p.LastError
			}
			if !p.Checked.IsZero() {
				s += ", checked " + ago(p.Checked, e.now)
			}
			if p.Backup != "" {
				s += ", backup " + p.Backup
			}
			line("%s", s)
		}
	}

	// The settings and the log need administrator rights.
	line("")
	if raw, err := config.Read(e.cfgPath); err == nil {
		line("Settings (%s):", e.cfgPath)
		line("  server: %s", raw.Server)
		if raw.AgentName != "" {
			line("  till name: %s", raw.AgentName)
		}
		ppi := "default (96, as a browser prints)"
		if raw.HTMLPxPerInch != 0 {
			ppi = strconv.FormatFloat(raw.HTMLPxPerInch, 'f', -1, 64)
		}
		line("  bill drawing scale (html_px_per_inch): %s", ppi)
		for _, p := range raw.Printers {
			line("  printer: %s", p.Describe())
		}
	} else {
		line("Settings:      not included (%s)", readWhy(err))
	}
	appendTail := func(title, path string, n int) {
		tail, err := tailLines(path, n)
		if err != nil {
			line("%s: not included (%s)", title, readWhy(err))
			return
		}
		line("%s (%s, last %d lines):", title, path, len(tail))
		for _, l := range tail {
			line("  %s", l)
		}
	}
	line("")
	appendTail("Settings changes", filepath.Join(filepath.Dir(e.cfgPath), "logs", "config-changes.log"), 10)
	line("")
	appendTail("Log", logPath(e.cfgPath), 40)
	return b.String()
}

func readWhy(err error) string {
	switch {
	case errors.Is(err, os.ErrPermission):
		return "needs administrator rights: use the button in the setup window to include it"
	case errors.Is(err, os.ErrNotExist):
		return "there isn't one yet"
	}
	return err.Error()
}

var versionDir = regexp.MustCompile(`^\d+(\.\d+){3}$`)

// edgeVersion is Edge's version, from the folder named after it beside
// msedge.exe (Edge keeps its files in Application\<version>\). With two
// (during an update), the newer.
func edgeVersion(exe string) string {
	entries, err := os.ReadDir(filepath.Dir(exe))
	if err != nil {
		return "version unknown"
	}
	var vs []string
	for _, e := range entries {
		if e.IsDir() && versionDir.MatchString(e.Name()) {
			vs = append(vs, e.Name())
		}
	}
	if len(vs) == 0 {
		return "version unknown"
	}
	sort.Slice(vs, func(i, j int) bool { return versionLess(vs[i], vs[j]) })
	return "version " + vs[len(vs)-1]
}

func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := range min(len(pa), len(pb)) {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x < y
		}
	}
	return len(pa) < len(pb)
}

// checkEdge draws a 60 mm black line the way the agent draws a bill for an
// 80 mm printer, and measures it in printer dots: 480 when Edge draws at full
// size.
func checkEdge(edge string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	doc := fmt.Sprintf(`<!doctype html><html><body style="margin:0"><div style="width:%dmm;height:5mm;background:#000"></div><p style="font:12px Arial">Palate edge check</p></body></html>`, edgeCheckMM)
	img, err := (&htmlrender.Edge{Path: edge}).Render(ctx, doc, 576)
	if err != nil {
		return 0, err
	}
	return widestInk(img), nil
}

// widestInk is the most dots of ink in any one row.
func widestInk(img image.Image) int {
	b := img.Bounds()
	best := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		n := 0
		for x := b.Min.X; x < b.Max.X; x++ {
			if htmlrender.Ink(img, x, y) {
				n++
			}
		}
		best = max(best, n)
	}
	return best
}

// reportFileName is the name the report is saved under.
func reportFileName(now time.Time) string {
	return "Palate support report " + now.Local().Format("2006-01-02 1504") + ".txt"
}

// saveReport writes the report into dir (the Desktop), with Windows line
// endings so Notepad shows it properly, and returns its path.
func saveReport(dir, report string, now time.Time) (string, error) {
	path := filepath.Join(dir, reportFileName(now))
	text := strings.ReplaceAll(report, "\n", "\r\n")
	return path, os.WriteFile(path, []byte(text), 0o644)
}

// makeSupportReport gathers the report on this PC.
func makeSupportReport(cfgPath string) string {
	self, _ := os.Executable()
	return supportReport(reportEnv{
		now: time.Now(), cfgPath: cfgPath, svc: service.Status,
		edgeFind: (&htmlrender.Edge{}).Find, edgeCheck: checkEdge,
		self: self, windows: windowsVersion(),
		installed: func() string {
			c, err := compareInstalled(self, installedExe(), runVersion)
			switch {
			case err != nil:
				return ""
			case strings.HasPrefix(c.Installed, "missing"):
				return "not in " + filepath.Dir(installedExe())
			case !c.Different:
				return "this file is the installed program"
			}
			return c.Installed + " (a different file from this one)"
		},
	})
}
