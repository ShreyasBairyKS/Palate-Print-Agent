package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/config"
)

const devServer = "https://development-plexus.palatepower.example/api/print-agent"
const prodServer = "https://plexus.palatepower.example/api/print-agent"

// serverTill writes a till's config pointing at devServer and returns a setup
// whose pairing records the address it was asked to pair with.
func serverTill(t *testing.T, pairErr error) (*setupEnv, *[]string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.json")
	raw := &config.Config{Server: devServer, AgentName: "FRONT-TILL", Printers: []config.Printer{
		{ID: "p-tandoor", Name: "Tandoor", Kind: "network", Address: "192.168.1.50:9100", Width: 80}}}
	if err := config.Save(path, raw); err != nil {
		t.Fatal(err)
	}
	var asked []string
	env := &setupEnv{cfgPath: path, pair: func(cfg *config.Config, code string) (string, error) {
		asked = append(asked, cfg.Server+" "+code)
		if pairErr != nil {
			return "", pairErr
		}
		return "dev-0042", nil
	}}
	return env, &asked
}

func savedServer(t *testing.T, env *setupEnv) string {
	t.Helper()
	c, err := config.Read(env.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	return c.Server
}

// Moving a till to another server pairs with the new one, then saves the new
// address and notes the change.
func TestPairServerMovesTheTill(t *testing.T) {
	env, asked := serverTill(t, nil)
	dev, moved, err := pairServer(env, "  "+prodServer+"  ", "K7Q2-M9XD")
	if err != nil || dev != "dev-0042" || !moved {
		t.Fatalf("pairServer = %q, %v, %v", dev, moved, err)
	}
	if len(*asked) != 1 || (*asked)[0] != prodServer+" K7Q2-M9XD" {
		t.Fatalf("paired with %v, want the new server", *asked)
	}
	if got := savedServer(t, env); got != prodServer {
		t.Fatalf("saved server = %q, want %q", got, prodServer)
	}
	log, err := os.ReadFile(filepath.Join(filepath.Dir(env.cfgPath), "logs", "config-changes.log"))
	if err != nil || !strings.Contains(string(log), "server changed from "+devServer+" to "+prodServer) {
		t.Fatalf("change log = %q, %v", log, err)
	}
	// The printers are untouched.
	if c, _ := config.Read(env.cfgPath); len(c.Printers) != 1 || c.Printers[0].ID != "p-tandoor" {
		t.Fatalf("printers = %+v", c.Printers)
	}
}

// A refused code, or a server that can't be reached, changes nothing: the
// till keeps its old address.
func TestPairServerFailureKeepsTheOldAddress(t *testing.T) {
	env, asked := serverTill(t, errors.New("pairing refused (401 invalid_code): code is not valid"))
	if _, moved, err := pairServer(env, prodServer, "WRONG"); err == nil || moved {
		t.Fatalf("pairServer = %v, %v; want the pairing error", moved, err)
	}
	if len(*asked) != 1 {
		t.Fatalf("pairing calls = %v", *asked)
	}
	if got := savedServer(t, env); got != devServer {
		t.Fatalf("saved server = %q; a failed pairing must keep %q", got, devServer)
	}
}

// An address the agent would refuse is caught before anything is sent.
func TestPairServerRefusesABadAddress(t *testing.T) {
	for _, bad := range []string{"", "http://plexus.palatepower.example/api/print-agent", prodServer + "/v1", "plexus.palatepower.example"} {
		env, asked := serverTill(t, nil)
		if _, _, err := pairServer(env, bad, "K7Q2-M9XD"); err == nil {
			t.Errorf("%q: accepted", bad)
		}
		if len(*asked) != 0 {
			t.Errorf("%q: paired anyway", bad)
		}
		if got := savedServer(t, env); got != devServer {
			t.Errorf("%q: saved server = %q", bad, got)
		}
	}
}

// Pairing again with the same address (a trailing slash doesn't count as a
// change) just pairs.
func TestPairServerSameAddress(t *testing.T) {
	env, asked := serverTill(t, nil)
	if _, moved, err := pairServer(env, devServer+"/", "K7Q2-M9XD"); err != nil || moved {
		t.Fatalf("pairServer = %v, %v", moved, err)
	}
	if len(*asked) != 1 {
		t.Fatalf("pairing calls = %v", *asked)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(env.cfgPath), "logs", "config-changes.log")); err == nil {
		t.Fatal("no change, but one was recorded")
	}
}
