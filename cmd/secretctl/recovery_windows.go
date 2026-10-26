package main

import (
	"github.com/JeremyProffittOrg/suburban-remote/internal/securelocal"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func secureRecoveryDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	for current := path; ; current = filepath.Dir(current) {
		ptr, err := windows.UTF16PtrFromString(current)
		if err != nil {
			return err
		}
		attrs, err := windows.GetFileAttributes(ptr)
		if err != nil {
			return err
		}
		if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return windows.ERROR_ACCESS_DENIED
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}
func protectRecovery(data []byte) ([]byte, error)   { return securelocal.Protect(data) }
func unprotectRecovery(data []byte) ([]byte, error) { return securelocal.Unprotect(data) }
