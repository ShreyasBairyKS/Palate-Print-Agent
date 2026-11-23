//go:build !windows

package config

import "github.com/ShreyasBairyKS/Palate-Print-Agent/internal/fileacl"

const needAdminHow = "run it again with sudo in front"

// handToService gives a config written by root to the service's own user
// (where there is one), readable by it only.
func handToService(path string) error { return fileacl.Restrict(path) }
