package main

import (
	"fmt"
	"strings"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/config"
)

// sameServer reports whether two server addresses are the same, ignoring
// spaces around them and a trailing slash.
func sameServer(a, b string) bool {
	norm := func(s string) string { return strings.TrimRight(strings.TrimSpace(s), "/") }
	return norm(a) == norm(b)
}

// pairServer pairs this till using code with the Palate server at server,
// and saves server as the config's address once pairing has worked.
//
// A till moving to another Palate server (development to production, say)
// needs a credential from that server. Pairing comes first, so a wrong
// address or a bad code changes nothing: the old address stays in the
// config. Only a successful pairing replaces the credential, and then the
// new address is saved to match. It returns the device id the server gave,
// and whether the address changed.
func pairServer(env *setupEnv, server, code string) (dev string, moved bool, err error) {
	server = strings.TrimSpace(server)
	if err := config.CheckServer(server); err != nil {
		return "", false, fmt.Errorf("Palate's server address: %w", err)
	}
	cfg, err := config.Load(env.cfgPath)
	if err != nil {
		return "", false, err
	}
	old := cfg.Server
	moved = !sameServer(old, server)
	cfg.Server = server
	dev, err = env.pair(cfg, code)
	if err != nil {
		return "", false, err
	}
	if !moved {
		return dev, false, nil
	}
	raw, err := config.Read(env.cfgPath)
	if err == nil {
		raw.Server = server
		err = config.Save(env.cfgPath, raw)
	}
	if err != nil {
		return dev, false, fmt.Errorf("paired with %s as %s, but couldn't save the new address, so the agent still uses %s: %w", server, dev, old, err)
	}
	recordChange(env.cfgPath, "server changed from "+old+" to "+server+" (paired as "+dev+")")
	return dev, true, nil
}
