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

func TestRestrictDirLetsTheServiceInAndBroadGroupsOut(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	inherited := filepath.Join(dir, "agent.json")
	if err := os.WriteFile(inherited, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unlock(t, dir) })
	if err := RestrictDir(dir); err != nil {
		if runtime.GOOS == "windows" && strings.Contains(err.Error(), "Access is denied") {
			t.Skip("needs to own the folder")
		}
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		return
	}
	for _, p := range []string{dir, inherited} {
		desc, err := Describe(p)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %s", filepath.Base(p), desc)
		if !strings.Contains(desc, ServiceSID(ServiceName)) {
			t.Fatalf("%s: access list %q does not include the service account", p, desc)
		}
		for _, broad := range []string{";;;BU)", ";;;AU)", ";;;WD)"} {
			if strings.Contains(desc, broad) {
				t.Fatalf("%s: access list %q still lets a broad group in (%s)", p, desc, broad)
			}
		}
	}
}
