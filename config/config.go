// Package config is the till's local configuration: where the server is,
// which printers this till owns and how to reach them, and the timings.
//
// Printer addresses live only here. They are set during setup, confirmed by a
// test print, and never sent to or received from the cloud.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/job"
)

// Config is the agent's local config file (JSON).
type Config struct {
	Server         string    `json:"server"`          // https://..., or http:// on loopback for the rig
	AgentName      string    `json:"agent_name"`      // shown in the portal; defaults to the hostname
	CredentialFile string    `json:"credential_file"` // defaults to credential.bin beside the config
	Printers       []Printer `json:"printers"`
	Timings        Timings   `json:"timings"`
}

// Printer is one printer this till owns.
type Printer struct {
	ID      string `json:"id"`      // what jobs name it by
	Name    string `json:"name"`    // "Tandoor", printed on MOVED FROM banners
	Kind    string `json:"kind"`    // "network" (USB arrives in M2)
	Address string `json:"address"` // "192.168.1.50" or "192.168.1.50:9100"
	Width   int    `json:"width"`   // 58 or 80
}

// Timings are the agent's tunable numbers. Zero values take the defaults
// from the design plan.
type Timings struct {
	ConnectTimeout Duration `json:"connect_timeout"` // default 3s
	StallTimeout   Duration `json:"stall_timeout"`   // write with no progress; default 10s
	StatusTimeout  Duration `json:"status_timeout"`  // wait for a DLE EOT reply; default 500ms
	FastRetries    int      `json:"fast_retries"`    // retries after a refused connection; default 2
	RetryGap       Duration `json:"retry_gap"`       // default 1s
	DelayedAfter   Duration `json:"delayed_after"`   // DELAYED banner past this age; default 10m
}

// Duration is a time.Duration written as "3s" in JSON.
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string like \"3s\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

func (d Duration) D() time.Duration { return time.Duration(d) }

func (t *Timings) applyDefaults() {
	def := func(d *Duration, v time.Duration) {
		if *d == 0 {
			*d = Duration(v)
		}
	}
	def(&t.ConnectTimeout, 3*time.Second)
	def(&t.StallTimeout, 10*time.Second)
	def(&t.StatusTimeout, 500*time.Millisecond)
	def(&t.RetryGap, time.Second)
	def(&t.DelayedAfter, 10*time.Minute)
	if t.FastRetries == 0 {
		t.FastRetries = 2
	}
}

// Load reads, defaults and validates a config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.CredentialFile == "" {
		c.CredentialFile = filepath.Join(filepath.Dir(path), "credential.bin")
	}
	if c.AgentName == "" {
		c.AgentName, _ = os.Hostname()
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// Validate applies defaults and checks every rule. Printer addresses are
// normalised to host:port.
func (c *Config) Validate() error {
	c.Timings.applyDefaults()
	if err := checkServer(c.Server); err != nil {
		return err
	}
	if len(c.Printers) == 0 {
		return errors.New("no printers configured")
	}
	seen := map[string]bool{}
	for i := range c.Printers {
		p := &c.Printers[i]
		if p.ID == "" || p.Name == "" {
			return fmt.Errorf("printers[%d]: id and name are required", i)
		}
		if seen[p.ID] {
			return fmt.Errorf("printers[%d]: duplicate id %q", i, p.ID)
		}
		seen[p.ID] = true
		if !job.Width(p.Width).Valid() {
			return fmt.Errorf("printer %q: width must be 58 or 80", p.ID)
		}
		switch p.Kind {
		case "network":
			addr, err := CheckPrinterAddress(p.Address)
			if err != nil {
				return fmt.Errorf("printer %q: %w", p.ID, err)
			}
			p.Address = addr
		case "usb":
			return fmt.Errorf("printer %q: USB printers arrive in M2", p.ID)
		default:
			return fmt.Errorf("printer %q: kind must be \"network\"", p.ID)
		}
	}
	return nil
}

// CheckPrinterAddress accepts only an IP literal in a private range
// (10/8, 172.16/12, 192.168/16) or loopback (for the test rig), with an
// optional port (default 9100). Hostnames are refused: DNS could point one
// anywhere. A public IP is never accepted.
func CheckPrinterAddress(s string) (string, error) {
	host, port := s, "9100"
	if h, p, err := net.SplitHostPort(s); err == nil {
		host, port = h, p
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return "", fmt.Errorf("address %q must be an IP address, not a hostname", s)
	}
	ip = ip.Unmap()
	if !ip.Is4() || !(ip.IsPrivate() || ip.IsLoopback()) {
		return "", fmt.Errorf("address %q is not in a private range (10/8, 172.16/12, 192.168/16)", s)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("address %q has a bad port", s)
	}
	return net.JoinHostPort(ip.String(), port), nil
}

// checkServer requires https, except plain http to a loopback address for
// the mock server.
func checkServer(s string) error {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return fmt.Errorf("server %q is not a URL", s)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if ip, err := netip.ParseAddr(u.Hostname()); err == nil && ip.IsLoopback() {
			return nil
		}
		return fmt.Errorf("server %q: plain http is only allowed to a loopback address", s)
	}
	return fmt.Errorf("server %q must use https", s)
}
