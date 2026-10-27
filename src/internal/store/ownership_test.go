// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sagar2395/snowopslabs/internal/runowner"
)

func TestRecoverOwner(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       Status
		ownerState   string
		wantStatus   Status
		wantRecovery RecoveryState
		wantError    bool
	}{
		{name: "dead queued owner", status: StatusQueued, ownerState: "dead", wantStatus: StatusCancelled, wantRecovery: RecoveryNone},
		{name: "dead running owner", status: StatusRunning, ownerState: "dead", wantStatus: StatusRunning, wantRecovery: RecoveryRequired},
		{name: "live owner", status: StatusRunning, ownerState: "live", wantStatus: StatusRunning, wantRecovery: RecoveryNone},
		{name: "missing owner file", status: StatusRunning, ownerState: "missing", wantStatus: StatusRunning, wantRecovery: RecoveryNone, wantError: true},
		{name: "legacy queued", status: StatusQueued, wantStatus: StatusQueued, wantRecovery: RecoveryRequired},
		{name: "legacy running", status: StatusRunning, wantStatus: StatusRunning, wantRecovery: RecoveryRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			st := newStore(t)
			var owner *runowner.Lock
			ownerID := ""
			if tc.ownerState != "" {
				var err error
				owner, err = runowner.New(ctx, st.Path())
				if err != nil {
					t.Fatal(err)
				}
				ownerID = owner.ID
				defer owner.Close()
			}
			if err := st.CreateRun(ctx, Run{ID: "work", Kind: "test", LockKey: "key", OwnerID: ownerID}); err != nil {
				t.Fatal(err)
			}
			if tc.status == StatusRunning {
				if err := st.StartRun(ctx, "work", time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := st.AppendLogs(ctx, "work", []LogLine{{Text: "partial output"}}); err != nil {
				t.Fatal(err)
			}
			if tc.ownerState == "dead" {
				if err := owner.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if tc.ownerState == "missing" {
				dir, err := runowner.Directory(st.Path())
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(dir, owner.ID)); err != nil {
					t.Fatal(err)
				}
			}
			_, err := st.RecoverOrphanedRuns(ctx, time.Now())
			if (err != nil) != tc.wantError {
				t.Fatalf("recovery error = %v; want error %v", err, tc.wantError)
			}
			rec, err := st.GetRun(ctx, "work")
			if err != nil {
				t.Fatal(err)
			}
			if rec.Status != tc.wantStatus || rec.RecoveryState != tc.wantRecovery || rec.OwnerID != ownerID {
				t.Fatalf("recovered run = %+v", rec)
			}
			_, held, err := st.ActiveRunForLock(ctx, "key")
			if err != nil || held == tc.wantStatus.Terminal() {
				t.Fatalf("key held = %v, %v", held, err)
			}
			before, err := st.ReadLogs(ctx, "work", 0, 100)
			if err != nil || len(before) == 0 || before[0].Text != "partial output" {
				t.Fatalf("partial logs = %+v, %v", before, err)
			}
			if !tc.wantError {
				if _, err := st.RecoverOrphanedRuns(ctx, time.Now()); err != nil {
					t.Fatal(err)
				}
				after, err := st.ReadLogs(ctx, "work", 0, 100)
				if err != nil || len(after) != len(before) {
					t.Fatalf("repeated recovery duplicated logs: %+v, %v", after, err)
				}
			}
			if tc.ownerState == "live" || tc.ownerState == "missing" {
				if err := st.FinishOwnedRun(ctx, "work", ownerID, StatusSucceeded, nil, "", time.Now(), time.Second); err != nil {
					t.Fatalf("probe revoked owner's write access: %v", err)
				}
			}
		})
	}
}

func interruptedRun(t *testing.T, st *Store) string {
	t.Helper()
	owner, err := runowner.New(t.Context(), st.Path())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(t.Context(), Run{ID: "work", Kind: "test", LockKey: "key", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	if err := st.StartOwnedRun(t.Context(), "work", owner.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendLogs(t.Context(), "work", []LogLine{{Text: "partial output"}}); err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecoverOrphanedRuns(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	return owner.ID
}

func TestReleaseRecoveredRun(t *testing.T) {
	ctx := t.Context()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	st := newStore(t, WithClock(func() time.Time { return now }))
	owner := interruptedRun(t, st)
	released, err := st.ReleaseRecoveredRun(ctx, "work", "uid:501 (operator)")
	if err != nil {
		t.Fatal(err)
	}
	if released.Status != StatusCancelled || released.RecoveryState != RecoveryReleased || released.RecoveryReason != "forced_release" || released.OwnerID != owner || released.ReleasedBy != "uid:501 (operator)" || !released.ReleasedAt.Equal(now) {
		t.Fatalf("release = %+v", released)
	}
	if err := st.FinishOwnedRun(ctx, "work", owner, StatusSucceeded, nil, "old completion", now, time.Second); err == nil {
		t.Fatal("old completion overwrote the operator's release")
	}
	if err := st.CreateRun(ctx, Run{ID: "next", Kind: "test", LockKey: "key"}); err != nil {
		t.Fatalf("key not released: %v", err)
	}
	if _, err := st.ReleaseRecoveredRun(ctx, "work", "uid:999 (different)"); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetRun(ctx, "work")
	if err != nil || got.ReleasedBy != released.ReleasedBy || !got.ReleasedAt.Equal(released.ReleasedAt) || got.Error != released.Error {
		t.Fatalf("repeated release changed audit fields: %+v, %v", got, err)
	}
	audit, err := st.ListAudit(ctx, 20)
	if err != nil || len(audit) != 1 || audit[0].Action != "run.recover" || audit[0].Actor != released.ReleasedBy || audit[0].RunID != "work" || !strings.Contains(audit[0].Detail, owner) {
		t.Fatalf("audit = %+v, %v", audit, err)
	}
	logs, err := st.ReadLogs(ctx, "work", 0, 100)
	if err != nil || len(logs) != 3 || logs[0].Text != "partial output" || !strings.Contains(logs[2].Text, "operation may still be running") {
		t.Fatalf("logs = %+v, %v", logs, err)
	}
}

func TestReleaseRecoveredRunRefusals(t *testing.T) {
	for _, tc := range []struct{ name string }{
		{name: "empty actor"}, {name: "live owner"}, {name: "missing owner file"},
		{name: "audit write fails"}, {name: "not recovery required"}, {name: "missing run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			st := newStore(t)
			owner := interruptedRun(t, st)
			id, actor := "work", "uid:501"
			switch tc.name {
			case "empty actor":
				actor = ""
			case "live owner":
				lock, err := runowner.Probe(ctx, st.Path(), owner)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
			case "missing owner file":
				dir, err := runowner.Directory(st.Path())
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(dir, owner)); err != nil {
					t.Fatal(err)
				}
			case "audit write fails":
				if _, err := st.DB().ExecContext(ctx, `DROP TABLE audit`); err != nil {
					t.Fatal(err)
				}
			case "not recovery required":
				if _, err := st.DB().ExecContext(ctx, `UPDATE runs SET recovery_state = 'none' WHERE id = 'work'`); err != nil {
					t.Fatal(err)
				}
			case "missing run":
				id = "missing"
			}
			before, err := st.GetRun(ctx, "work")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.ReleaseRecoveredRun(ctx, id, actor); err == nil {
				t.Fatal("unsafe recovery succeeded")
			}
			after, err := st.GetRun(ctx, "work")
			if err != nil || after.Status != before.Status || after.RecoveryState != before.RecoveryState || after.ReleasedBy != "" || !after.ReleasedAt.IsZero() {
				t.Fatalf("failed release changed row: %+v, %v", after, err)
			}
			if err := st.CreateRun(ctx, Run{ID: "next", Kind: "test", LockKey: "key"}); !errors.Is(err, ErrLockHeld) {
				t.Fatalf("failed release lost key: %v", err)
			}
			logs, err := st.ReadLogs(ctx, "work", 0, 100)
			if err != nil || len(logs) != 2 {
				t.Fatalf("failed release changed logs: %+v, %v", logs, err)
			}
		})
	}
}

func TestConcurrentRecovery(t *testing.T) {
	ctx := t.Context()
	first := newStore(t)
	interruptedRun(t, first)
	second, err := Open(ctx, first.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	for _, st := range []*Store{first, second} {
		group.Go(func() {
			<-start
			_, err := st.ReleaseRecoveredRun(ctx, "work", "uid:501")
			results <- err
		})
	}
	close(start)
	group.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success == 0 {
		t.Fatal("neither recovery completed")
	}
	audit, err := first.ListAudit(ctx, 100)
	if err != nil || len(audit) != 1 {
		t.Fatalf("concurrent recoveries recorded %+v, %v", audit, err)
	}
	if _, err := second.ReleaseRecoveredRun(ctx, "work", "uid:999"); err != nil {
		t.Fatal(err)
	}
	audit, err = first.ListAudit(ctx, 100)
	if err != nil || len(audit) != 1 || audit[0].Actor != "uid:501" {
		t.Fatalf("repeat changed audit: %+v, %v", audit, err)
	}
}

func TestStoreRetainsWorkers(t *testing.T) {
	st := newStore(t)
	release, err := st.Retain()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SchemaVersion(t.Context()); err != nil {
		t.Fatalf("store closed before worker returned: %v", err)
	}
	if _, err := runowner.Upgrade(t.Context(), st.Path(), true); !errors.Is(err, runowner.ErrBusy) {
		t.Fatalf("migration entered retained store: %v", err)
	}
	if _, err := st.Retain(); err == nil {
		t.Fatal("closing store accepted another borrower")
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SchemaVersion(t.Context()); err == nil {
		t.Fatal("store stayed open after final worker returned")
	}
	lock, err := runowner.Upgrade(t.Context(), st.Path(), true)
	if err != nil {
		t.Fatalf("upgrade lock not released: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyOwnerMigration(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	legacy := &Store{db: db, path: path, now: time.Now}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.version < 3 {
			if err := legacy.applyMigration(ctx, m); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, status := range []Status{StatusQueued, StatusRunning} {
		if _, err := db.ExecContext(ctx, `INSERT INTO runs (id, kind, lock_key, status, queued_at) VALUES (?, 'test', ?, ?, 1)`, string(status), string(status), status); err != nil {
			t.Fatal(err)
		}
		if _, err := legacy.AppendLogs(ctx, string(status), []LogLine{{Text: "legacy output"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, status := range []Status{StatusQueued, StatusRunning} {
		t.Run(string(status), func(t *testing.T) {
			rec, err := st.GetRun(ctx, string(status))
			if err != nil || rec.Status != status || rec.OwnerID != "" || rec.RecoveryState != RecoveryRequired || rec.RecoveryReason != "legacy_upgrade" {
				t.Fatalf("legacy recovery = %+v, %v", rec, err)
			}
			logs, err := st.ReadLogs(ctx, rec.ID, 0, 100)
			if err != nil || len(logs) != 1 || logs[0].Text != "legacy output" {
				t.Fatalf("legacy logs = %+v, %v", logs, err)
			}
			if _, err := st.ReleaseRecoveredRun(ctx, rec.ID, "uid:501"); err != nil {
				t.Fatal(err)
			}
			if _, held, err := st.ActiveRunForLock(ctx, string(status)); err != nil || held {
				t.Fatalf("legacy key retained: %v, %v", held, err)
			}
		})
	}
}

func TestMigrationRefusesUncertainOwners(t *testing.T) {
	for _, state := range []string{"busy", "missing", "not regular"} {
		t.Run(state, func(t *testing.T) {
			st := newStore(t)
			owner, err := runowner.New(t.Context(), st.Path())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = owner.Close() }()
			if err := st.CreateRun(t.Context(), Run{ID: "held", Kind: "test", LockKey: "lab", OwnerID: owner.ID}); err != nil {
				t.Fatal(err)
			}
			if _, err := st.db.ExecContext(t.Context(), `DELETE FROM schema_migrations WHERE version = 3`); err != nil {
				t.Fatal(err)
			}
			path := st.Path()
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			if state != "busy" {
				if err := owner.Close(); err != nil {
					t.Fatal(err)
				}
				dir, err := runowner.Directory(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(dir, owner.ID)); err != nil {
					t.Fatal(err)
				}
				if state == "not regular" {
					if err := os.Mkdir(filepath.Join(dir, owner.ID), 0o700); err != nil {
						t.Fatal(err)
					}
				}
			}
			opened, err := Open(t.Context(), path)
			if opened != nil {
				_ = opened.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "migration cannot check owner") {
				t.Fatalf("migration bypassed owner check: %v", err)
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var status, recovery string
			if err := db.QueryRowContext(t.Context(), `SELECT status, recovery_state FROM runs WHERE id = 'held'`).Scan(&status, &recovery); err != nil {
				t.Fatal(err)
			}
			if status != string(StatusQueued) || recovery != string(RecoveryNone) {
				t.Fatalf("refused migration changed work: %s/%s", status, recovery)
			}
			version, err := currentVersion(t.Context(), db)
			if err != nil || version != 2 {
				t.Fatalf("refused migration changed version: %d, %v", version, err)
			}
		})
	}
}

func TestMigrationRequiresOfflineStore(t *testing.T) {
	st := newStore(t)
	if _, err := st.db.ExecContext(t.Context(), `DELETE FROM schema_migrations WHERE version = 3`); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(t.Context(), st.Path())
	if opened != nil {
		_ = opened.Close()
	}
	if !errors.Is(err, runowner.ErrBusy) || !strings.Contains(err.Error(), "stop all labctl processes") {
		t.Fatalf("online migration = %v", err)
	}
	if version, err := st.SchemaVersion(t.Context()); err != nil || version != 2 {
		t.Fatalf("live store lost access: %d, %v", version, err)
	}
}

func TestOwnedWritesRequireAnOwner(t *testing.T) {
	st := newStore(t)
	if err := st.CreateRun(t.Context(), Run{ID: "held", Kind: "test", LockKey: "lab"}); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []struct {
		name  string
		write func() error
	}{
		{name: "start", write: func() error { return st.StartOwnedRun(t.Context(), "held", "", time.Now()) }},
		{name: "finish", write: func() error {
			return st.FinishOwnedRun(t.Context(), "held", "", StatusSucceeded, nil, "", time.Now(), time.Second)
		}},
	} {
		t.Run(operation.name, func(t *testing.T) {
			if err := operation.write(); err == nil {
				t.Fatal("owned write accepted an empty owner")
			}
			rec, err := st.GetRun(t.Context(), "held")
			if err != nil || rec.Status != StatusQueued {
				t.Fatalf("refused write changed run: %+v, %v", rec, err)
			}
		})
	}
}

func TestReleaseReportsStoredTimestamp(t *testing.T) {
	at := time.Date(2026, 10, 27, 12, 0, 0, 123456789, time.FixedZone("operator", 9*60*60))
	st := newStore(t, WithClock(func() time.Time { return at }))
	interruptedRun(t, st)
	released, err := st.ReleaseRecoveredRun(t.Context(), "work", "uid=123")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := st.GetRun(t.Context(), "work")
	if err != nil {
		t.Fatal(err)
	}
	if !released.ReleasedAt.Equal(stored.ReleasedAt) || !released.EndedAt.Equal(stored.EndedAt) {
		t.Fatalf("reported release time %v differs from stored %v", released.ReleasedAt, stored.ReleasedAt)
	}
	if repeated, err := st.ReleaseRecoveredRun(t.Context(), "work", "uid=456"); err != nil || !repeated.ReleasedAt.Equal(released.ReleasedAt) {
		t.Fatalf("repeated release time changed: %+v, %v", repeated, err)
	}
}

func TestOwnerOperationsRespectContext(t *testing.T) {
	st := newStore(t)
	owner := interruptedRun(t, st)
	for _, tc := range []struct {
		name        string
		makeContext func() (context.Context, context.CancelFunc)
	}{
		{name: "cancelled", makeContext: func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			return ctx, cancel
		}},
		{name: "deadline", makeContext: func() (context.Context, context.CancelFunc) {
			return context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := tc.makeContext()
			defer cancel()
			calls := []struct {
				name string
				run  func() error
			}{
				{name: "list owners", run: func() error { _, err := st.ActiveOwners(ctx); return err }},
				{name: "recover owners", run: func() error { _, err := st.RecoverOrphanedRuns(ctx, time.Now()); return err }},
				{name: "release", run: func() error { _, err := st.ReleaseRecoveredRun(ctx, "work", "uid:501"); return err }},
				{name: "worker start", run: func() error { return st.StartOwnedRun(ctx, "work", owner, time.Now()) }},
				{name: "worker finish", run: func() error { return st.FinishOwnedRun(ctx, "work", owner, StatusSucceeded, nil, "", time.Now(), 0) }},
			}
			for _, call := range calls {
				t.Run(call.name, func(t *testing.T) {
					if err := call.run(); !errors.Is(err, ctx.Err()) {
						t.Fatalf("operation = %v; want %v", err, ctx.Err())
					}
				})
			}
		})
	}
}
