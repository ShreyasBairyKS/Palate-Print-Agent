// Package render is the paper track: it turns one station's slip into the
// exact ESC/POS bytes a printer will accept.
//
// Rules (enforced by arch_test.go at the repo root):
//   - no sockets: this package never imports net or net/http
//   - no config, no credentials: everything it needs is in its arguments
//   - deterministic: the same slip and width give byte-identical output on
//     every till, which is what the golden tests check
package render

import (
	"errors"

	"palate-print-agent/job"
)

// ErrNotImplemented is returned until M1 delivers Latin slips.
var ErrNotImplemented = errors.New("render: not implemented yet (M1)")

// Render turns one station's slip into the exact bytes a printer will accept.
// It opens no socket, reads no config, and never sees a credential.
func Render(slip job.Slip, width job.Width) ([]byte, error) {
	if !width.Valid() {
		return nil, errors.New("render: width must be 58 or 80 mm")
	}
	return nil, ErrNotImplemented
}
