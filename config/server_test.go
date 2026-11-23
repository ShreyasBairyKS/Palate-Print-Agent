package config

import "testing"

func TestNormalizeServer(t *testing.T) {
	for in, want := range map[string]string{
		"https://plexus.palatepower.com/":                            DefaultServer,
		"https://plexus.palatepower.com":                             DefaultServer,
		"  plexus.palatepower.com  ":                                 DefaultServer,
		"HTTPS://Plexus.PalatePower.com/":                            DefaultServer,
		"https://plexus.palatepower.com/api/print-agent/":            DefaultServer,
		"https://plexus.palatepower.com/api/print-agent/v1":          DefaultServer,
		"https://plexus.palatepower.com/api/print-agent/v1/":         DefaultServer,
		"https://development-plexus.palatepower.com/api/print-agent": "https://development-plexus.palatepower.com/api/print-agent",
		"https://example.com/other/path":                             "https://example.com/other/path",
		"http://127.0.0.1:8080/":                                     "http://127.0.0.1:8080",
		"http://localhost:8080":                                      "http://localhost:8080",
		"https://example.com/v1":                                     "https://example.com/api/print-agent",
		"":                                                           "",
	} {
		if got := NormalizeServer(in); got != want {
			t.Errorf("NormalizeServer(%q) = %q, want %q", in, got, want)
		}
		if in != "" && in != "http://localhost:8080" { // CheckServer wants an IP for plain http
			if err := CheckServer(NormalizeServer(in)); err != nil {
				t.Errorf("CheckServer(NormalizeServer(%q)): %v", in, err)
			}
		}
	}
}

func TestNormalizeServerLeavesPlainHTTPForCheckServer(t *testing.T) {
	got := NormalizeServer("http://plexus.palatepower.com/")
	if CheckServer(got) == nil {
		t.Fatalf("%q passed CheckServer; plain http to a public host must be refused", got)
	}
}
