//go:build !windows

package fileacl

import "os"

// Restrict makes the file readable and writable by its owner only.
func Restrict(path string) error { return os.Chmod(path, 0o600) }

// ReadableByLocalUsers makes the file writable by its owner and readable by everyone.
func ReadableByLocalUsers(path string) error { return os.Chmod(path, 0o644) }

// RestrictDir makes the folder usable by its owner only.
func RestrictDir(path string) error { return os.Chmod(path, 0o700) }

// Describe returns the file's permission bits.
func Describe(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	return fi.Mode().Perm().String(), nil
}
