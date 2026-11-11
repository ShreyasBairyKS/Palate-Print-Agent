//go:build !windows

package dpapi

import "errors"

// The agent runs only on Windows tills. On other systems callers get an error
// rather than a plaintext fallback.

var errNoDPAPI = errors.New("DPAPI is only available on Windows")

// Protect always fails off Windows.
func Protect(_, _ []byte) ([]byte, error) { return nil, errNoDPAPI }

// Unprotect always fails off Windows.
func Unprotect(_, _ []byte) ([]byte, error) { return nil, errNoDPAPI }
