package fileacl

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRestrictLeavesOnlyTheAgentAndAdmins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.bin")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Restrict(path); err != nil {
		t.Fatal(err)
	}
	desc, err := Describe(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("access list: %s", desc)
	if runtime.GOOS == "windows" {
		if !strings.HasPrefix(desc, "D:P") {
			t.Fatalf("access list %q still inherits from the folder", desc)
		}
		// BU = Users, AU = Authenticated Users, WD = Everyone.
		for _, broad := range []string{";;;BU)", ";;;AU)", ";;;WD)"} {
			if strings.Contains(desc, broad) {
				t.Fatalf("access list %q still lets a broad group in (%s)", desc, broad)
			}
		}
	} else if desc != "-rw-------" {
		t.Fatalf("permissions %s, want -rw-------", desc)
	}
	// The account running the agent can still use the file.
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("the agent's own account lost access: %v", err)
	}
	if _, err := os.ReadFile(path); err != nil {
		t.Fatalf("the agent's own account lost access: %v", err)
	}
}
