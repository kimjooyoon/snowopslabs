// SPDX-License-Identifier: Apache-2.0

// Package runowner holds local file locks for engine ownership and schema upgrades.
package runowner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ErrBusy means another open descriptor holds an incompatible lock.
var ErrBusy = errors.New("run owner lock is busy")

// Lock keeps its descriptor open until Close. Owner files are never removed.
type Lock struct {
	ID       string
	mu       sync.Mutex
	file     *os.File
	closeErr error
}

// Directory returns the owner directory beside the canonical database path.
// A new database requires an existing parent directory.
func Directory(database string) (string, error) {
	if database == "" {
		return "", errors.New("database path is required for run owner locks")
	}
	absolute, err := filepath.Abs(database)
	if err != nil {
		return "", fmt.Errorf("resolving database path: %w", err)
	}
	if _, err := os.Lstat(absolute); err == nil {
		canonical, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			return "", fmt.Errorf("resolving database file: %w", err)
		}
		return canonical + ".owners", nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("reading database path: %w", err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", fmt.Errorf("resolving database directory: %w", err)
	}
	return filepath.Join(parent, filepath.Base(absolute)) + ".owners", nil
}

func prepareDirectory(database string) (string, error) {
	dir, err := Directory(database)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil { //nolint:gosec // Owner storage is beside the operator-selected canonical database path.
		return "", fmt.Errorf("creating owner directory: %w", err)
	}
	info, err := os.Lstat(dir) //nolint:gosec // The operator selects the local database location; Directory resolves its owner directory.
	if err != nil {
		return "", fmt.Errorf("reading owner directory: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("owner directory must be a directory, not a symlink")
	}
	return dir, nil
}

// New creates a fresh owner file and holds its exclusive nonblocking lock.
// Go-created file descriptors are close-on-exec and are not passed to children.
func New(ctx context.Context, database string) (*Lock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := prepareDirectory(database)
	if err != nil {
		return nil, err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, fmt.Errorf("creating run owner ID: %w", err)
	}
	id := "owner_" + hex.EncodeToString(random[:])
	lock, err := open(ctx, filepath.Join(dir, id), os.O_RDWR|os.O_CREATE|os.O_EXCL, true)
	if err != nil {
		return nil, err
	}
	lock.ID = id
	return lock, nil
}

// Probe locks an existing owner file without creating or replacing it.
// ErrBusy leaves the live owner's descriptor and write access unchanged.
func Probe(ctx context.Context, database, id string) (*Lock, error) {
	if len(id) != len("owner_")+32 || !strings.HasPrefix(id, "owner_") {
		return nil, errors.New("invalid run owner ID")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(id, "owner_")); err != nil || strings.ToLower(id) != id {
		return nil, errors.New("invalid run owner ID")
	}
	dir, err := Directory(database)
	if err != nil {
		return nil, err
	}
	lock, err := open(ctx, filepath.Join(dir, id), os.O_RDWR, true)
	if err != nil {
		return nil, err
	}
	lock.ID = id
	return lock, nil
}

// Upgrade takes the shared store-lifetime lock or exclusive migration lock.
// Both forms refuse contention instead of waiting for another process.
func Upgrade(ctx context.Context, database string, exclusive bool) (*Lock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := prepareDirectory(database)
	if err != nil {
		return nil, err
	}
	return open(ctx, filepath.Join(dir, "upgrade.lock"), os.O_RDWR|os.O_CREATE, exclusive)
}

func open(ctx context.Context, path string, flags int, exclusive bool) (*Lock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil { //nolint:gosec // Callers use the canonical database directory and validated owner IDs or the fixed upgrade filename.
		if !info.Mode().IsRegular() {
			return nil, errors.New("run owner lock must be a regular file")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("reading run owner lock: %w", err)
	}
	file, err := os.OpenFile(path, flags, 0o600) //nolint:gosec // The lock path is constructed from the operator-selected canonical database location and a validated filename.
	if err != nil {
		return nil, fmt.Errorf("opening run owner lock: %w", err)
	}
	if err := lockFile(file, exclusive); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return &Lock{file: file}, nil
}

// Shared downgrades an exclusive migration lock for the store's lifetime.
func (l *Lock) Shared(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return os.ErrClosed
	}
	return lockFile(l.file, false)
}

// Close releases the descriptor exactly once while preserving its owner file.
func (l *Lock) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		l.closeErr = l.file.Close()
		l.file = nil
	}
	return l.closeErr
}
