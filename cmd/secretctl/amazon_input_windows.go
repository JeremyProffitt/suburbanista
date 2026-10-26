package main

import (
	"bufio"
	"bytes"
	"errors"
	"golang.org/x/sys/windows"
	"io"
	"os"
)

func readAmazonInput() ([]byte, error) {
	handle := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if windows.GetConsoleMode(handle, &mode) != nil {
		return nil, errors.New("run this helper in an attended Windows console")
	}
	if windows.SetConsoleMode(handle, mode&^windows.ENABLE_ECHO_INPUT) != nil {
		return nil, errors.New("cannot disable console echo")
	}
	defer windows.SetConsoleMode(handle, mode)
	value, err := bufio.NewReader(io.LimitReader(os.Stdin, 4097)).ReadBytes('\n')
	if err != nil {
		clear(value)
		return nil, errors.New("cannot read hidden input")
	}
	return bytes.TrimRight(value, "\r\n"), nil
}
