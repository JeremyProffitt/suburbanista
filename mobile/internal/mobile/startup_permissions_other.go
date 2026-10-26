//go:build !android

package mobile

import "fyne.io/fyne/v2"

func platformPermissionInventory(fyne.Window) []permissionGrant { return nil }
func platformRequestPermissions(fyne.Window, []string) bool     { return false }
