package agent

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPClientRefusesRedirects(t *testing.T) {
	var sawCredential bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawCredential = r.Header.Get("Authorization") != ""
	}))
	defer elsewhere.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/jobs/poll", nil)
	req.Header.Set("Authorization", "Bearer secret")
	_, err := NewHTTPClient(0).Do(req)
	if !errors.Is(err, ErrRedirect) {
		t.Fatalf("err = %v; want ErrRedirect", err)
	}
	if sawCredential {
		t.Fatal("the redirect target received the credential")
	}
}

func TestHTTPClientNeedsTLS12(t *testing.T) {
	tr := NewHTTPClient(0).Transport.(*http.Transport)
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Fatal("the client must refuse anything older than TLS 1.2")
	}
	if tr.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("certificate checks must stay on")
	}
}
