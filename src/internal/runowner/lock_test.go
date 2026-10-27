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
