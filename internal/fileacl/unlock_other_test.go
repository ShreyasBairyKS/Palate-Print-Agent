//go:build !windows

package fileacl

import "testing"

func unlock(*testing.T, string) {}
