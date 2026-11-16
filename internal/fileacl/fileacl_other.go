//go:build !windows

package fileacl

import "os"

// Restrict makes the file readable and writable by its owner only.
func Restrict(path string) error { return os.Chmod(path, 0o600) }

// Describe returns the file's permission bits.
func Describe(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	return fi.Mode().Perm().String(), nil
}
