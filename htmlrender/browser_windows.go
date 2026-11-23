//go:build windows

package htmlrender

import (
	"os"
	"path/filepath"
)

const noBrowser = "Microsoft Edge not found"

// browserCandidates are where Edge is installed, machine-wide or per user.
func browserCandidates() []string {
	var out []string
	for _, v := range []string{"ProgramFiles(x86)", "ProgramFiles", "LocalAppData"} {
		if d := os.Getenv(v); d != "" {
			out = append(out, filepath.Join(d, "Microsoft", "Edge", "Application", "msedge.exe"))
		}
	}
	return out
}

func platformArgs() []string { return nil }
