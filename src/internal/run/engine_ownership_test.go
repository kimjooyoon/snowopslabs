// SPDX-License-Identifier: Apache-2.0

package run

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/sagar2395/snowopslabs/internal/runowner"
	"github.com/sagar2395/snowopslabs/internal/store"
	"github.com/sagar2395/snowopslabs/internal/toolchain"
)

func TestStartPreservesAnotherEngine(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
	}{
		{name: "locked operation", key: "lab:default"},
		{name: "unlocked operation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			path := filepath.Join(t.TempDir(), "runs.db")
			resolver, err := toolchain.NewResolver(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			newEngine := func() (*Engine, *store.Store) {
				t.Helper()
				st, err := store.Open(ctx, path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = st.Close() })
				eng, err := New(st, toolchain.NewFake(), resolver, WithWorkers(1))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
					defer cancel()
					if err := eng.Shutdown(shutdown); err != nil {
						t.Error(err)
					}
				})
				return eng, st
			}
			first, st := newEngine()
			if err := first.Start(ctx); err != nil {
				t.Fatal(err)
			}
			started := make(chan struct{})
			id, err := first.Submit(ctx, Spec{
				Kind: "test.live", LockKey: tc.key,
				Func: func(ctx context.Context, _ io.Writer) error {
					close(started)
					<-ctx.Done()
					return ctx.Err()
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("first engine did not start its operation")
			}
			if _, err := st.AppendLogs(ctx, id, []store.LogLine{{Text: "owner is still executing"}}); err != nil {
				t.Fatal(err)
			}

			second, other := newEngine()
			if err := second.Start(ctx); err != nil {
				t.Fatal(err)
			}
			rec, err := other.GetRun(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if rec.Status != store.StatusRunning {
				t.Errorf("second engine changed live run to %q; want running", rec.Status)
			}
			lines, err := other.ReadLogs(ctx, id, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range lines {
				if line.Text == "run interrupted: labctl exited while this run was in flight" {
					t.Error("live run was given an interruption record")
				}
			}
			if tc.key != "" {
				_, err := second.Submit(ctx, Spec{Kind: "test.conflict", LockKey: tc.key,
					Func: func(context.Context, io.Writer) error { return nil }})
				var conflict *LockConflictError
				if !errors.As(err, &conflict) || conflict.Holder.ID != id {
					t.Errorf("same-key operation = %v; want conflict with %s", err, id)
				}
			}
			probe, err := second.Submit(ctx, Spec{Kind: "test.unlocked",
				Func: func(context.Context, io.Writer) error { return nil }})
			if err != nil {
				t.Fatal(err)
			}
			(&harness{t: t, store: other}).waitForStatus(probe, store.StatusSucceeded)
		})
	}
}

func TestShutdownTimeoutRetainsOwnership(t *testing.T) {
	h := newHarness(t)
	started, cancelled, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-finish:
		default:
			close(finish)
		}
	}()
	id, err := h.engine.Submit(t.Context(), Spec{Kind: "test.cleanup", LockKey: "lab", Func: func(ctx context.Context, _ io.Writer) error {
		close(started)
		<-ctx.Done()
		close(cancelled)
		<-finish
		return ctx.Err()
	}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not start")
	}
	rec, err := h.store.GetRun(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := h.engine.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown while cleanup is held = %v", err)
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("shutdown did not request cancellation")
	}
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.GetRun(t.Context(), id); err != nil {
		t.Fatalf("worker store closed early: %v", err)
	}
	if lock, err := runowner.Probe(t.Context(), h.store.Path(), rec.OwnerID); !errors.Is(err, runowner.ErrBusy) {
		_ = lock.Close()
		t.Fatalf("owner lock released before cleanup: %v", err)
	}
	if lock, err := runowner.Upgrade(t.Context(), h.store.Path(), true); !errors.Is(err, runowner.ErrBusy) {
		_ = lock.Close()
		t.Fatalf("store lifetime lock released before cleanup: %v", err)
	}
	other, err := store.Open(t.Context(), h.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	if changed, err := other.RecoverOrphanedRuns(t.Context(), time.Now()); err != nil || len(changed) != 0 {
		t.Fatalf("timed-out shutdown permitted recovery: %v, %v", changed, err)
	}
	if err := other.CreateRun(t.Context(), store.Run{ID: "too-early", Kind: "test", LockKey: "lab"}); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("cleanup lost key: %v", err)
	}
	close(finish)
	ctx, cancel = context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := h.engine.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := other.GetRun(t.Context(), id)
	if err != nil || after.Status != store.StatusCancelled {
		t.Fatalf("worker did not persist result: %+v, %v", after, err)
	}
	if _, err := h.store.GetRun(t.Context(), id); err == nil {
		t.Fatal("closed store stayed open after cleanup")
	}
	lock, err := runowner.Probe(t.Context(), h.store.Path(), rec.OwnerID)
	if err != nil {
		t.Fatalf("completed owner remained locked: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStartFailureCanBeRetried(t *testing.T) {
	for _, mode := range []string{"cancelled", "deadline", "closed store"} {
		t.Run(mode, func(t *testing.T) {
			st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "runs.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			resolver, err := toolchain.NewResolver(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			engine, err := New(st, toolchain.NewFake(), resolver)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			want := context.Canceled
			switch mode {
			case "cancelled":
				cancel()
			case "deadline":
				cancel()
				ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
				want = context.DeadlineExceeded
			case "closed store":
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
			}
			defer cancel()
			if err := engine.Start(ctx); err == nil || mode != "closed store" && !errors.Is(err, want) {
				t.Fatalf("failed start = %v", err)
			}
			if mode != "closed store" {
				if err := engine.Start(t.Context()); err != nil {
					t.Fatalf("retry start = %v", err)
				}
				if err := engine.Start(t.Context()); err == nil {
					t.Fatal("accepted duplicate start")
				}
			}
			if err := engine.Shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := engine.Start(t.Context()); err == nil {
				t.Fatal("accepted start after shutdown")
			}
		})
	}
}
