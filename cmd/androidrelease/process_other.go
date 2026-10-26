//go:build !windows

package main

import "os/exec"

func hideAndroidCommand(*exec.Cmd) {}
