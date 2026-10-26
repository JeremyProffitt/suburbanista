//go:build !windows

package securelocal

import "errors"

func Protect([]byte) ([]byte, error)   { return nil, errors.New("Windows DPAPI required") }
func Unprotect([]byte) ([]byte, error) { return nil, errors.New("Windows DPAPI required") }
