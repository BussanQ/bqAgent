//go:build !windows

package session

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func TryLockFile(file *os.File, exclusive bool) error {
	kind := unix.LOCK_SH
	if exclusive {
		kind = unix.LOCK_EX
	}
	err := unix.Flock(int(file.Fd()), kind|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return ErrContextBusy
	}
	return err
}
