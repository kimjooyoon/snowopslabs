// SPDX-License-Identifier: Apache-2.0

package runowner

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDirectory(t *testing.T) {
	dir := t.TempDir()
	database := filepath.Join(dir, "runs.db")
	if err := os.WriteFile(database, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias.db")
	if err := os.Symlink(database, alias); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(database)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, path string }{
		{name: "existing database", path: database},
		{name: "database symlink", path: alias},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Directory(tc.path)
			if err != nil || got != want+".owners" {
				t.Fatalf("Directory = %q, %v; want %q", got, err, want+".owners")
			}
		})
	}
	for _, tc := range []struct{ name, path string }{
		{name: "empty database"},
		{name: "missing parent", path: filepath.Join(dir, "absent", "runs.db")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Directory(tc.path); err == nil {
				t.Fatal("invalid database path succeeded")
			}
		})
	}
	newPath := filepath.Join(dir, "new.db")
	if _, err := Directory(newPath); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerProbe(t *testing.T) {
	ctx := t.Context()
	database := filepath.Join(t.TempDir(), "runs.db")
	owner, err := New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	if _, err := Probe(ctx, database, owner.ID); !errors.Is(err, ErrBusy) {
		t.Fatalf("probe live owner = %v; want busy", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	probe, err := Probe(ctx, database, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	dir, err := Directory(database)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, owner.ID)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("owner file was removed: %v", err)
	}
	other, err := New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if other.ID == owner.ID {
		t.Fatal("owner ID was reused")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Probe(ctx, database, owner.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing owner probe = %v; want missing file", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("probe recreated owner file: %v", err)
	}
	for _, id := range []string{"", "../escape", "owner_" + strings.Repeat("A", 32), "owner_" + strings.Repeat("z", 32)} {
		t.Run("invalid_"+id, func(t *testing.T) {
			if _, err := Probe(ctx, database, id); err == nil {
				t.Fatal("invalid owner ID was accepted")
			}
		})
	}
}

func TestUpgradeLock(t *testing.T) {
	ctx := t.Context()
	database := filepath.Join(t.TempDir(), "runs.db")
	first, err := Upgrade(ctx, database, false)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Upgrade(ctx, database, false)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err := Upgrade(ctx, database, true); !errors.Is(err, ErrBusy) {
		t.Fatalf("migration while stores are open = %v; want busy", err)
	}
	if err := errors.Join(first.Close(), second.Close()); err != nil {
		t.Fatal(err)
	}
	migration, err := Upgrade(ctx, database, true)
	if err != nil {
		t.Fatal(err)
	}
	defer migration.Close()
	if _, err := Upgrade(ctx, database, false); !errors.Is(err, ErrBusy) {
		t.Fatalf("open during migration = %v; want busy", err)
	}
	if err := migration.Shared(ctx); err != nil {
		t.Fatal(err)
	}
	reader, err := Upgrade(ctx, database, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := migration.Close(); err != nil {
		t.Fatal(err)
	}
	if err := migration.Shared(ctx); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("downgrade closed lock = %v", err)
	}
}

func TestCancelledLocks(t *testing.T) {
	database := filepath.Join(t.TempDir(), "runs.db")
	for _, tc := range []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
	}{
		{name: "cancelled", ctx: func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			return ctx, cancel
		}},
		{name: "expired", ctx: func() (context.Context, context.CancelFunc) {
			return context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := tc.ctx()
			defer cancel()
			if _, err := New(ctx, database); !errors.Is(err, ctx.Err()) {
				t.Fatalf("New = %v; want %v", err, ctx.Err())
			}
			if _, err := Upgrade(ctx, database, true); !errors.Is(err, ctx.Err()) {
				t.Fatalf("Upgrade = %v; want %v", err, ctx.Err())
			}
			if _, err := Probe(ctx, database, "owner_"+strings.Repeat("a", 32)); !errors.Is(err, ctx.Err()) {
				t.Fatalf("Probe = %v; want %v", err, ctx.Err())
			}
		})
	}
}

func TestRejectNonregularFiles(t *testing.T) {
	for _, tc := range []struct {
		name    string
		symlink bool
	}{
		{name: "directory"}, {name: "symlink", symlink: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database := filepath.Join(t.TempDir(), "runs.db")
			owner, err := New(t.Context(), database)
			if err != nil {
				t.Fatal(err)
			}
			if err := owner.Close(); err != nil {
				t.Fatal(err)
			}
			dir, err := Directory(database)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, owner.ID)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if tc.symlink {
				if err := os.Symlink(filepath.Join(dir, "missing"), path); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if _, err := Probe(t.Context(), database, owner.ID); err == nil {
				t.Fatal("nonregular owner was accepted")
			}
		})
	}
}

func TestCleanup(t *testing.T) {
	ctx := t.Context()
	database := filepath.Join(t.TempDir(), "runs.db")
	var referenced []string
	for _, tc := range []struct {
		name             string
		referenced, busy bool
	}{
		{name: "unused"}, {name: "referenced", referenced: true}, {name: "busy", busy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner, err := New(ctx, database)
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Close()
			if tc.referenced {
				referenced = append(referenced, owner.ID)
			}
			if !tc.busy {
				if err := owner.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if err := Cleanup(ctx, database, referenced); err != nil {
				t.Fatal(err)
			}
			dir, err := Directory(database)
			if err != nil {
				t.Fatal(err)
			}
			_, err = os.Stat(filepath.Join(dir, owner.ID))
			keep := tc.referenced || tc.busy
			if keep && err != nil || !keep && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("owner retained=%v: %v", keep, err)
			}
		})
	}
	upgrade, err := Upgrade(ctx, database, false)
	if err != nil {
		t.Fatal(err)
	}
	defer upgrade.Close()
	if err := Cleanup(ctx, database, referenced); err != nil {
		t.Fatal(err)
	}
	dir, err := Directory(database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "upgrade.lock")); err != nil {
		t.Fatalf("cleanup removed upgrade lock: %v", err)
	}
}

func TestCleanupFailures(t *testing.T) {
	for _, tc := range []struct{ name string }{
		{name: "cancelled"}, {name: "deadline"}, {name: "empty database"},
		{name: "missing owner directory"}, {name: "nonregular owner"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			database := filepath.Join(t.TempDir(), "runs.db")
			var want error
			switch tc.name {
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
				want = context.Canceled
			case "deadline":
				expired, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
				ctx = expired
				want = context.DeadlineExceeded
			case "empty database":
				database = ""
			case "nonregular owner":
				owner, err := New(ctx, database)
				if err != nil {
					t.Fatal(err)
				}
				if err := owner.Close(); err != nil {
					t.Fatal(err)
				}
				dir, err := Directory(database)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(dir, owner.ID)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := Cleanup(ctx, database, nil); err == nil || want != nil && !errors.Is(err, want) {
				t.Fatalf("cleanup = %v; want error %v", err, want)
			}
		})
	}
}

func TestRejectUnlinkedOwnerRegistration(t *testing.T) {
	ctx := t.Context()
	database := filepath.Join(t.TempDir(), "runs.db")
	dir, err := prepareDirectory(database)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "owner_"+strings.Repeat("a", 32))
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := Cleanup(ctx, database, nil); err != nil {
		t.Fatal(err)
	}
	lock, err := lockOpenedFile(ctx, file, true)
	if err == nil {
		_ = lock.Close()
		t.Fatal("registered an owner whose file cleanup removed before flock")
	}
	if !errors.Is(err, errLockReplaced) {
		t.Fatalf("unlinked registration = %v; want changed-file refusal", err)
	}
	if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("refused registration left descriptor open: %v", err)
	}
	owner, err := New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if _, err := Probe(ctx, database, owner.ID); !errors.Is(err, ErrBusy) {
		t.Fatalf("fresh owner does not hold its visible file: %v", err)
	}
}

func TestRejectReplacedOwnerRegistration(t *testing.T) {
	for _, directory := range []bool{false, true} {
		t.Run(map[bool]string{false: "different inode", true: "directory"}[directory], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "owner_"+strings.Repeat("a", 32))
			file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if directory {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			lock, err := lockOpenedFile(t.Context(), file, true)
			if err == nil {
				_ = lock.Close()
				t.Fatal("registered a replaced owner file")
			}
			if !directory && !errors.Is(err, errLockReplaced) {
				t.Fatalf("replaced registration = %v", err)
			}
			if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("rejected descriptor remained open: %v", err)
			}
		})
	}
}

func TestOwnerLockSubprocess(t *testing.T) {
	mode := os.Getenv("SNOWOPS_RUNOWNER_TEST_MODE")
	if mode == "" {
		return
	}
	if mode == "hold" {
		owner, err := New(t.Context(), os.Getenv("SNOWOPS_RUNOWNER_TEST_DB"))
		if err != nil {
			t.Fatal(err)
		}
		defer owner.Close()
		if _, err := io.WriteString(os.Stdout, owner.ID+"\n"); err != nil {
			t.Fatal(err)
		}
	} else if _, err := io.WriteString(os.Stdout, "ready\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
}

func TestLockProcessLifetime(t *testing.T) {
	for _, tc := range []struct{ name, mode string }{
		{name: "owner process dies", mode: "hold"},
		{name: "child does not inherit descriptor", mode: "wait"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database := filepath.Join(t.TempDir(), "runs.db")
			owner, err := New(t.Context(), database)
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Close()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestOwnerLockSubprocess$", "-test.timeout=8s")
			cmd.Env = append(os.Environ(), "SNOWOPS_RUNOWNER_TEST_MODE="+tc.mode, "SNOWOPS_RUNOWNER_TEST_DB="+database)
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = stdin.Close()
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			})
			line, err := bufio.NewReader(stdout).ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			id := owner.ID
			if tc.mode == "hold" {
				id = strings.TrimSpace(line)
				if _, err := Probe(t.Context(), database, id); !errors.Is(err, ErrBusy) {
					t.Fatalf("child owner's lock = %v; want busy", err)
				}
				if err := cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				_ = cmd.Wait()
			} else if err := owner.Close(); err != nil {
				t.Fatal(err)
			}
			probe, err := Probe(t.Context(), database, id)
			if err != nil {
				t.Fatalf("released owner stays locked: %v", err)
			}
			if err := probe.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCleanupContinuesAfterEntryErrors(t *testing.T) {
	ctx := t.Context()
	database := filepath.Join(t.TempDir(), "runs.db")
	upgrade, err := Upgrade(ctx, database, false)
	if err != nil {
		t.Fatal(err)
	}
	defer upgrade.Close()
	dir, err := Directory(database)
	if err != nil {
		t.Fatal(err)
	}
	bad := "owner_" + strings.Repeat("0", 32)
	link := "owner_" + strings.Repeat("1", 32)
	unused := "owner_" + strings.Repeat("f", 32)
	if err := os.Mkdir(filepath.Join(dir, bad), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, link)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, unused), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err = Cleanup(ctx, database, nil)
	if err == nil || !strings.Contains(err.Error(), bad) || !strings.Contains(err.Error(), link) {
		t.Fatalf("cleanup did not report both entries: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, unused)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleanup stopped before unused owner: %v", err)
	}
	for _, id := range []string{bad, link, "upgrade.lock"} {
		if _, err := os.Lstat(filepath.Join(dir, id)); err != nil {
			t.Fatalf("cleanup changed retained entry %s: %v", id, err)
		}
	}
}
