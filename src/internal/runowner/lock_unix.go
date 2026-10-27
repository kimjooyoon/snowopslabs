//go:build darwin || linux

// SPDX-License-Identifier: Apache-2.0

package runowner

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func lockFile(file *os.File, exclusive bool) error {
	operation := unix.LOCK_SH | unix.LOCK_NB
	if exclusive {
		operation = unix.LOCK_EX | unix.LOCK_NB
	}
	if err := unix.Flock(int(file.Fd()), operation); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return ErrBusy
		}
		return fmt.Errorf("locking run owner file: %w", err)
	}
	return nil
}
