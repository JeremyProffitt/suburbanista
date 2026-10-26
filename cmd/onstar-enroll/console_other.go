//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
)

func hideProcess(*exec.Cmd) {}
func killBrowserDriver(pid int) {
	if pid > 0 {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
	}
}
func checkConsole() error {
	return errors.New("attended enrollment requires Windows protected recovery")
}
func readHidden() ([]byte, error) { return nil, checkConsole() }
