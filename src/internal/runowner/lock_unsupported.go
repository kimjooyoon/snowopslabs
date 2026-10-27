//go:build !darwin && !linux

// SPDX-License-Identifier: Apache-2.0

package runowner

import (
	"errors"
	"os"
)

func lockFile(_ *os.File, _ bool) error {
	return errors.New("run owner locks require macOS or Linux; use a Linux home in WSL2")
}
