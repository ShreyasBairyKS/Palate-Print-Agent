//go:build windows

package config

const needAdminHow = `run this from a terminal opened with "Run as administrator"`

// handToService does nothing on Windows: the new file inherits the data
// folder's access list, which includes the service.
func handToService(string) error { return nil }
