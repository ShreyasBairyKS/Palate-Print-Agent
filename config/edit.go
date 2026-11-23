package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Read decodes a config file exactly as written: no defaults filled in and
// no validation. It is the starting point for an edit, so that saving writes
// back only what the person wrote, plus the change.
func Read(path string) (*Config, error) {
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
	return &c, nil
}

// Check validates c as Load would, on a copy, so c itself keeps only what
// was written. It returns the validated copy.
func (c *Config) Check() (*Config, error) {
	v := *c
	v.Printers = slices.Clone(c.Printers)
	if err := v.Validate(); err != nil {
		return nil, err
	}
	return &v, nil
}

// Save writes c to path, replacing the file in one step: it is written to a
// temporary file beside it and renamed over it, so the agent never reads a
// half-written config. The file it replaces is kept as path + ".previous",
// so a change can be undone. The new file inherits the folder's access list,
// like the old one.
//
// c must pass Check; Save refuses one that doesn't.
func Save(path string, c *Config) error {
	if _, err := c.Check(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".agent-*.json")
	if err != nil {
		return writeErr(err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return writeErr(err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return writeErr(err)
	}
	if err := tmp.Close(); err != nil {
		return writeErr(err)
	}
	if err := handToService(tmp.Name()); err != nil {
		return writeErr(err)
	}
	if old, err := os.ReadFile(path); err == nil {
		if err := os.WriteFile(path+".previous", old, 0o600); err != nil {
			return writeErr(err)
		}
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return writeErr(err)
	}
	return nil
}

// ErrNeedAdmin means the config folder can't be written by this account.
// The data folder is closed to everyone but administrators and the service,
// so changes are made with administrator rights (needAdminHow says how).
var ErrNeedAdmin = errors.New("can't write the config: " + needAdminHow)

func writeErr(err error) error {
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("%w (%v)", ErrNeedAdmin, err)
	}
	return err
}

// Find returns the printer with this id, or nil.
func (c *Config) Find(id string) *Printer {
	for i := range c.Printers {
		if c.Printers[i].ID == id {
			return &c.Printers[i]
		}
	}
	return nil
}

// Remove deletes the printer with this id. It refuses to remove the last
// printer, or one another printer names as its backup.
func (c *Config) Remove(id string) error {
	i := slices.IndexFunc(c.Printers, func(p Printer) bool { return p.ID == id })
	if i < 0 {
		return fmt.Errorf("no printer with id %q on this till", id)
	}
	if len(c.Printers) == 1 {
		return errors.New("it's the only printer on this till; a till needs at least one")
	}
	var users []string
	for _, p := range c.Printers {
		if p.Backup == id {
			users = append(users, p.ID)
		}
	}
	if len(users) > 0 {
		return fmt.Errorf("%s use it as their backup; change their backup first", strings.Join(users, ", "))
	}
	c.Printers = slices.Delete(c.Printers, i, i+1)
	return nil
}

// Describe is one line about a printer, for listings and change logs.
func (p Printer) Describe() string {
	where := p.Address
	if p.Kind == "usb" {
		where = "Windows printer \"" + p.PrinterName + "\""
	}
	s := fmt.Sprintf("%s (%s): %s %s, %s", p.ID, p.Name, p.Kind, where, p.Paper())
	if p.Backup != "" {
		s += ", backup " + p.Backup
	}
	return s
}
