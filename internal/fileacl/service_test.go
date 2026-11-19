package fileacl

import "testing"

// The expected value is what `sc.exe showsid PalatePrintAgent` prints.
func TestServiceSIDMatchesWindows(t *testing.T) {
	const want = "S-1-5-80-2254053686-245242420-1920714495-2489598368-3694679401"
	if got := ServiceSID(ServiceName); got != want {
		t.Fatalf("ServiceSID = %s, want %s", got, want)
	}
	if ServiceSID("palateprintagent") != want {
		t.Fatal("the service SID must not depend on the name's case")
	}
}
