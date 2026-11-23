package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/config"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/pair"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/printer"
)

func TestExplainKeepsTheCode(t *testing.T) {
	for _, c := range []struct {
		err        error
		code, word string
	}{
		{&pair.RefusedError{Status: 401, Code: "invalid_code", Message: "bad code"}, "401 invalid_code", "expired"},
		{fmt.Errorf("pairing: %w", &pair.RefusedError{Status: 401, Code: "invalid_code"}), "401 invalid_code", "Add a till"},
		{&pair.RefusedError{Status: 403}, "403", config.APIPath},
		{&pair.RefusedError{Status: 404}, "404", "no Palate print service"},
		{&pair.RefusedError{Status: 429, Code: "rate_limited"}, "429 rate_limited", "Wait a minute"},
		{&pair.RefusedError{Status: 503}, "503", "Try again in a few minutes"},
		{&pair.RefusedError{Status: 400, Code: "odd", Message: "something"}, "400 odd", "something"},
		{&printer.Error{Kind: printer.PaperOut, Err: errors.New("x")}, "paper out", "new roll"},
		{fmt.Errorf("test print on p: %w", &printer.Error{Kind: printer.Hang, Err: errors.New("i/o timeout")}), "no answer", "switched on"},
		{&printer.Error{Kind: printer.Refused, Err: errors.New("x")}, "refused", "IP address"},
		{&net.DNSError{Err: "no such host", Name: "plexus.example"}, "dns: plexus.example", "internet"},
		{context.DeadlineExceeded, "timeout", "didn't answer"},
		{config.ErrNeedAdmin, "access denied", "choose Yes"},
		{errors.New("dial tcp 1.2.3.4:443: connectex: No connection could be made because the target machine actively refused it."), "connection refused", "internet"},
	} {
		p := explain(c.err)
		if p.Code != c.code {
			t.Errorf("explain(%v).Code = %q, want %q", c.err, p.Code, c.code)
		}
		if !strings.Contains(p.What, c.word) {
			t.Errorf("explain(%v).What = %q, want it to mention %q", c.err, p.What, c.word)
		}
		if !strings.Contains(p.String(), "(error code: "+c.code+")") {
			t.Errorf("String() = %q lost the code", p.String())
		}
		if !strings.Contains(p.Box(), "Error code: "+c.code) || !strings.Contains(p.Box(), c.err.Error()) {
			t.Errorf("Box() = %q, want the code and the details", p.Box())
		}
	}
}

func TestExplainUnknownKeepsItsMessage(t *testing.T) {
	p := explain(errors.New("something odd happened"))
	if p.What != "Something odd happened." || p.Code != "" || p.String() != p.What {
		t.Fatalf("explain = %+v", p)
	}
	if explain(nil) != (plainError{}) {
		t.Fatal("explain(nil) isn't empty")
	}
}

func TestExplainText(t *testing.T) {
	for msg, code := range map[string]string{
		`Post "https://x/v1/jobs/poll": dial tcp: lookup x: no such host`: "dns",
		"poll: 401 unauthorized: credential revoked":                      "unauthorized",
		"poll: repair_needed": "unauthorized",
		`Post "https://x": context deadline exceeded (Client.Timeout exceeded)`: "timeout",
		"poll: server said (503 unavailable)":                                   "server error",
		"tls: failed to verify certificate: x509: certificate has expired":      "tls certificate",
	} {
		if p := explainText(msg); p.Code != code {
			t.Errorf("explainText(%q) = %+v, want code %q", msg, p, code)
		}
	}
}
