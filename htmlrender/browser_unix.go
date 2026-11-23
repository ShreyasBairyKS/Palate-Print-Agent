//go:build !windows

package htmlrender

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const noBrowser = "no Chromium browser (Microsoft Edge, Google Chrome or Chromium) found"

// browserCandidates are the Chromium browsers that draw bills, best first.
// On Linux a snap package (Ubuntu's Chromium) is skipped: it can't read the
// page from the agent's temporary folder.
func browserCandidates() []string {
	if runtime.GOOS == "darwin" {
		var out []string
		for _, app := range []string{"Microsoft Edge.app/Contents/MacOS/Microsoft Edge", "Google Chrome.app/Contents/MacOS/Google Chrome", "Chromium.app/Contents/MacOS/Chromium"} {
			out = append(out, filepath.Join("/Applications", app))
		}
		return out
	}
	var out []string
	for _, name := range []string{"microsoft-edge-stable", "microsoft-edge", "google-chrome-stable", "google-chrome", "chromium", "chromium-browser"} {
		p, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		if real, err := filepath.EvalSymlinks(p); err == nil && (strings.HasPrefix(real, "/snap/") || strings.HasSuffix(real, "/snap")) {
			continue
		}
		if strings.HasPrefix(p, "/snap/") {
			continue
		}
		out = append(out, p)
	}
	return out
}

// platformArgs: Chromium refuses to run as root with its sandbox on, so a
// service running as root (macOS, or Linux with -account localsystem)
// turns it off. The page is still locked down (see Lockdown) and can reach
// no network.
func platformArgs() []string {
	if os.Geteuid() == 0 {
		return []string{"--no-sandbox"}
	}
	return nil
}
