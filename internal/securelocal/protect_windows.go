// Package securelocal protects machine-local credentials using current-user DPAPI.
package securelocal

import (
	"golang.org/x/sys/windows"
	"unsafe"
)

func Protect(data []byte) ([]byte, error)   { return crypt(data, true) }
func Unprotect(data []byte) ([]byte, error) { return crypt(data, false) }
func crypt(data []byte, protect bool) ([]byte, error) {
	if len(data) == 0 {
		return nil, windows.ERROR_INVALID_DATA
	}
	input := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var output windows.DataBlob
	var err error
	if protect {
		err = windows.CryptProtectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	} else {
		err = windows.CryptUnprotectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	}
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data)))
	result := append([]byte(nil), unsafe.Slice(output.Data, int(output.Size))...)
	clear(unsafe.Slice(output.Data, int(output.Size)))
	return result, nil
}
