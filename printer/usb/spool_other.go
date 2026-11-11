//go:build !windows

package usbprinter

func spool(string, []byte) (int, string, error) { return 0, "opening printer", ErrUnsupported }

func list() ([]string, error) { return nil, ErrUnsupported }
