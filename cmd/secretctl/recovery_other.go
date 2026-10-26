//go:build !windows

package main

import (
	"errors"
	"github.com/JeremyProffittOrg/suburban-remote/internal/securelocal"
)

func secureRecoveryDirectory(string) error {
	return errors.New("Android recovery creation requires Windows DPAPI")
}
func protectRecovery(data []byte) ([]byte, error)   { return securelocal.Protect(data) }
func unprotectRecovery(data []byte) ([]byte, error) { return securelocal.Unprotect(data) }
