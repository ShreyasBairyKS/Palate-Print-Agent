package main

// Errors in plain words. The setup window, the console setup and the status
// window show what went wrong and what to do about it, in words for
// restaurant staff, and keep a short error code beside it for support.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/config"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/pair"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/printer"
)

// plainError is an error explained for people.
type plainError struct {
	What   string // what happened and what to do, in plain words
	Code   string // a short code to give support: "401 invalid_code", "paper out"
	Detail string // the full technical message
}

// String is the explanation with its code, on one line.
func (p plainError) String() string {
	if p.Code == "" {
		return p.What
	}
	return p.What + " (error code: " + p.Code + ")"
}

// Box is the explanation for a message box: the words, then the code and
// the technical details underneath for support.
func (p plainError) Box() string {
	s := p.What
	if p.Code != "" {
		s += "\n\nError code: " + p.Code
	}
	if p.Detail != "" && p.Detail != p.What {
		s += "\nDetails: " + p.Detail
	}
	return s
}

const (
	sayNewCode   = "In the Palate portal, choose \"Add a till\" for a new code and type it in."
	sayAddress   = "Use the address setup fills in (" + config.DefaultServer + "), or the one Palate gave you."
	sayInternet  = "Check this till is connected to the internet: open any website in the browser."
	sayPrinterOn = "Check the printer is switched on, its cable is in, and the IP address is right (hold the feed button while switching it on to print its address)."
)

// explain turns an error into plain words and a code. Unknown errors keep
// their own message.
func explain(err error) plainError {
	if err == nil {
		return plainError{}
	}
	detail := err.Error()
	pe := func(what, code string) plainError { return plainError{What: what, Code: code, Detail: detail} }

	var refused *pair.RefusedError
	if errors.As(err, &refused) {
		code := strings.TrimSpace(fmt.Sprintf("%d %s", refused.Status, refused.Code))
		switch s := refused.Status; {
		case s == http.StatusUnauthorized && (refused.Code == "invalid_code" || refused.Code == ""):
			return pe("That pairing code didn't work. It may be mistyped, already used, or expired (a code lasts 10 minutes). "+sayNewCode, code)
		case s == http.StatusUnauthorized:
			return pe("Palate didn't accept this pairing. "+sayNewCode, code)
		case s == http.StatusForbidden:
			return pe("Palate's server turned the connection away. This usually means the server address is incomplete: it should end in "+config.APIPath+". "+sayAddress, code)
		case s == http.StatusNotFound:
			return pe("There's no Palate print service at that address. "+sayAddress, code)
		case s == http.StatusTooManyRequests:
			return pe("Too many wrong codes in a row. Wait a minute, then try again with a new code.", code)
		case s >= 500:
			return pe("Palate's server has a problem right now. Try again in a few minutes; if it keeps happening, call Palate support.", code)
		}
		what := "Palate's server refused the pairing."
		if refused.Message != "" {
			what += " It said: " + refused.Message + "."
		}
		return pe(what, code)
	}

	switch printer.KindOf(err) {
	case printer.Refused:
		return pe("The printer refused the connection. "+sayPrinterOn, "refused")
	case printer.Hang:
		return pe("The printer didn't answer. "+sayPrinterOn+" It must be on the same network as this till.", "no answer")
	case printer.Partial:
		return pe("The printer stopped in the middle of the slip. Check its paper and that its cover is closed, then test again.", "partial write")
	case printer.PaperOut:
		return pe("The printer is out of paper, or its cover is open. Put in a new roll, close the cover, and test again.", "paper out")
	}

	if errors.Is(err, config.ErrNeedAdmin) || errors.Is(err, errNeedAdmin) || errors.Is(err, os.ErrPermission) {
		return pe("This needs administrator rights. "+sayAdmin, "access denied")
	}

	var dns *net.DNSError
	if errors.As(err, &dns) {
		return pe("This till can't find Palate's server. "+sayInternet+" If the internet works, check the server address. "+sayAddress, "dns: "+dns.Name)
	}
	var unknownCA x509.UnknownAuthorityError
	var badCert x509.CertificateInvalidError
	var badHost x509.HostnameError
	var verify *tls.CertificateVerificationError
	if errors.As(err, &unknownCA) || errors.As(err, &badCert) || errors.As(err, &badHost) || errors.As(err, &verify) {
		return pe("The secure connection to Palate failed. Check this PC's date and time are right. If they are, the restaurant's network may be blocking Palate: ask whoever looks after the internet.", "tls certificate")
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return pe("Palate's server couldn't be reached (the connection was refused). "+sayInternet+" Then check the server address.", "connection refused")
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return pe("Palate's server didn't answer in time. "+sayInternet+" Then try again.", "timeout")
	}
	return explainText(detail)
}

// explainText explains an error known only by its message, as the status
// file records the connection's last error.
func explainText(msg string) plainError {
	l := strings.ToLower(msg)
	pe := func(what, code string) plainError { return plainError{What: what, Code: code, Detail: msg} }
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(l, w) {
				return true
			}
		}
		return false
	}
	switch {
	case msg == "":
		return plainError{}
	case has("repair_needed", "re-pair", "unauthorized", "not paired"):
		return pe("This till needs pairing with Palate again. Open setup and type a new pairing code from the Palate portal (\"Add a till\").", "unauthorized")
	case has("no such host", "dns"):
		return pe("This till can't find Palate's server. "+sayInternet, "dns")
	case has("certificate", "x509", "tls:"):
		return pe("The secure connection to Palate failed. Check this PC's date and time are right.", "tls certificate")
	case has("timeout", "deadline exceeded", "timed out"):
		return pe("Palate's server isn't answering. "+sayInternet, "timeout")
	case has("refused", "actively refused"):
		return pe("Palate's server couldn't be reached. "+sayInternet, "connection refused")
	case has("(403", " 403", "403 "):
		return pe("Palate's server turned the connection away. Check the server address ends in "+config.APIPath+".", "403")
	case has("(404", " 404", "404 "):
		return pe("There's no Palate print service at that address. Check the server address.", "404")
	case has("(500", "(502", "(503", "(504", " 500", " 502", " 503", " 504"):
		return pe("Palate's server has a problem right now. Printing carries on once it's back.", "server error")
	case has("unreachable", "no route", "network is down", "connectex", "dial tcp"):
		return pe("This till can't reach the internet. "+sayInternet, "network")
	}
	what := strings.TrimSpace(msg)
	if what != "" {
		what = strings.ToUpper(what[:1]) + what[1:]
		if !strings.HasSuffix(what, ".") {
			what += "."
		}
	}
	return plainError{What: what, Detail: msg}
}
