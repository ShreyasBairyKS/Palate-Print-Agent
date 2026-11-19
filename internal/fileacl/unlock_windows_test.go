package fileacl

import "testing"

// unlock gives a restricted test folder back to the current account, which
// as its owner may always rewrite its access list, so the test can delete it.
func unlock(t *testing.T, path string) {
	sid, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	if err := setNamedDACL(path, "D:(A;OICI;FA;;;"+sid+")", false); err != nil {
		t.Fatal(err)
	}
}
