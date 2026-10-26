package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"golang.org/x/sys/windows"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func hideProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}

func killBrowserDriver(pid int) {
	if pid <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
	hideProcess(cmd)
	_ = cmd.Run()
}
func checkConsole() error {
	var mode uint32
	if windows.GetConsoleMode(windows.Handle(os.Stdin.Fd()), &mode) != nil {
		return errors.New("enrollment requires an attended Windows console")
	}
	return nil
}
func readHidden() ([]byte, error) {
	handle := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if windows.GetConsoleMode(handle, &mode) != nil {
		return nil, errors.New("attended console required")
	}
	if windows.SetConsoleMode(handle, mode&^windows.ENABLE_ECHO_INPUT) != nil {
		return nil, errors.New("cannot disable input echo")
	}
	defer windows.SetConsoleMode(handle, mode)
	value, err := bufio.NewReader(io.LimitReader(os.Stdin, 32769)).ReadBytes('\n')
	if err != nil || len(value) > 32768 {
		clear(value)
		return nil, errors.New("hidden input failed or exceeds limit")
	}
	return bytes.TrimRight(value, "\r\n"), nil
}
