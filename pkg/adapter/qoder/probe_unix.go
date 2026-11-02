//go:build !windows

package qoder

import (
	"os"
	"syscall"
)

// probeLeaseLocked checks if a lease file is actively held by another process via flock.
func probeLeaseLocked(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	// Probe non-blocking exclusive flock
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		// Cannot acquire lock -> an active process is holding it
		return true
	}
	// Acquired lock successfully -> no process is holding it. Release immediately.
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// lockFileExclusively acquires an exclusive non-blocking lock (used for testing).
func lockFileExclusively(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// unlockFile releases the lock (used for testing).
func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
