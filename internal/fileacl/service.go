package fileacl

import (
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"strings"
	"unicode/utf16"
)

// ServiceName is the agent's Windows service. Its virtual account,
// NT SERVICE\PalatePrintAgent, is what the service runs as.
const ServiceName = "PalatePrintAgent"

// ServiceSID is the SID of a service's virtual account, NT SERVICE\<name>.
// Windows derives it from the name alone (S-1-5-80 followed by the SHA-1 of
// the upper-cased name in UTF-16LE, as five 32-bit numbers), so it can be
// granted access before the service exists.
func ServiceSID(name string) string {
	u := utf16.Encode([]rune(strings.ToUpper(name)))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	h := sha1.Sum(b)
	sid := "S-1-5-80"
	for i := 0; i < 5; i++ {
		sid += fmt.Sprintf("-%d", binary.LittleEndian.Uint32(h[4*i:]))
	}
	return sid
}
