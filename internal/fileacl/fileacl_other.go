//go:build !windows

package fileacl

import (
	"os"
	"os/user"
	"strconv"
)

// ServiceUser is the Linux service's own system user (see package service).
// Files locked down by root are handed to it, so the service can read what
// setup wrote, the way Windows grants the service's account access. Where
// there is no such user (macOS, or a service running as root) files stay
// root's.
const ServiceUser = "palate-agent"

// owner gives path to the service user, when root is locking it down and
// that user exists.
func owner(path string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	u, err := user.Lookup(ServiceUser)
	if err != nil {
		return nil
	}
	uid, err1 := strconv.Atoi(u.Uid)
	gid, err2 := strconv.Atoi(u.Gid)
	if err1 != nil || err2 != nil {
		return nil
	}
	return os.Lchown(path, uid, gid)
}

// Restrict makes the file readable and writable by its owner (the service)
// only. Root can always read it.
func Restrict(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	return owner(path)
}

// ReadableByLocalUsers makes the file writable by its owner and readable by everyone.
func ReadableByLocalUsers(path string) error {
	if err := os.Chmod(path, 0o644); err != nil {
		return err
	}
	return owner(path)
}

// RestrictDir makes the folder usable by its owner (the service) only.
// Others may pass through it but not list it, so the status file (made
// ReadableByLocalUsers) can be read by its name, as on Windows.
func RestrictDir(path string) error {
	if err := os.Chmod(path, 0o711); err != nil {
		return err
	}
	return owner(path)
}

// Describe returns the file's permission bits.
func Describe(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	return fi.Mode().Perm().String(), nil
}
