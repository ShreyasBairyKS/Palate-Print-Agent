//go:build !windows

package discover

func windowsPorts() map[string]string { return map[string]string{} }
