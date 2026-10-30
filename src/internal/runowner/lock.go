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

var errLockReplaced = errors.New("run owner lock file changed before registration")

// Lock keeps its descriptor open until Close, including during owner-file cleanup.
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
	if _, err := os.Lstat(absolute); err == nil { //nolint:gosec // The operator selects the database path; resolving it establishes the matching owner directory.
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
	for range 3 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, fmt.Errorf("creating run owner ID: %w", err)
		}
		id := "owner_" + hex.EncodeToString(random[:])
		lock, err := open(ctx, filepath.Join(dir, id), os.O_RDWR|os.O_CREATE|os.O_EXCL, true)
		if errors.Is(err, errLockReplaced) || errors.Is(err, ErrBusy) {
			continue
		}
		if err != nil {
			return nil, err
		}
		lock.ID = id
		return lock, nil
	}
	return nil, errors.New("owner registration raced cleanup; retry the labctl command")
}

// Probe locks an existing owner file without creating or replacing it.
// ErrBusy leaves the live owner's descriptor and write access unchanged.
func Probe(ctx context.Context, database, id string) (*Lock, error) {
	if !validOwnerID(id) {
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

func validOwnerID(id string) bool {
	if len(id) != len("owner_")+32 || !strings.HasPrefix(id, "owner_") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(id, "owner_"))
	return err == nil && strings.ToLower(id) == id
}

// Cleanup removes unlocked owner files absent from the referenced owner snapshot.
// Held locks preserve active owners; registration retries a removed fresh file.
func Cleanup(ctx context.Context, database string, referenced []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := Directory(database)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("reading owner directory for cleanup: %w", err)
	}
	keep := make(map[string]bool, len(referenced))
	for _, id := range referenced {
		keep[id] = true
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		id := entry.Name()
		if !validOwnerID(id) || keep[id] {
			continue
		}
		lock, err := Probe(ctx, database, id)
		if errors.Is(err, ErrBusy) || errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("locking unused owner %s for cleanup: %w", id, err)
		}
		removeErr := os.Remove(filepath.Join(dir, id))
		if errors.Is(removeErr, fs.ErrNotExist) {
			removeErr = nil
		}
		if err := errors.Join(removeErr, lock.Close()); err != nil {
			return fmt.Errorf("removing unused owner %s: %w", id, err)
		}
	}
	return nil
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
	return lockOpenedFile(ctx, file, exclusive)
}

func lockOpenedFile(ctx context.Context, file *os.File, exclusive bool) (*Lock, error) {
	if err := lockFile(file, exclusive); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	opened, err := file.Stat()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("reading locked owner file: %w", err), file.Close())
	}
	visible, err := os.Lstat(file.Name()) //nolint:gosec // The open descriptor names the validated local lock path; this checks that its inode remains visible.
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			err = errors.Join(errLockReplaced, err)
		}
		return nil, errors.Join(fmt.Errorf("checking locked owner file: %w", err), file.Close())
	}
	if !visible.Mode().IsRegular() {
		return nil, errors.Join(errors.New("run owner lock must be a regular file"), file.Close())
	}
	if !os.SameFile(opened, visible) {
		return nil, errors.Join(errLockReplaced, file.Close())
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
