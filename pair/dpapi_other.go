//go:build !windows

package pair

import "errors"

// The agent runs only on Windows tills. On other systems FileStore refuses
// to work rather than fall back to plaintext; tests use MemStore.

var errNoDPAPI = errors.New("credential storage needs Windows DPAPI")

func protect([]byte) ([]byte, error)   { return nil, errNoDPAPI }
func unprotect([]byte) ([]byte, error) { return nil, errNoDPAPI }
