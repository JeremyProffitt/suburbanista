//go:build unix

package main

import (
	"golang.org/x/sys/unix"
	"os"
)

func acquireEnrollmentLock(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}
func releaseEnrollmentLock(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_UN) }
