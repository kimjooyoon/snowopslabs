// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Status is a run's lifecycle state. A CHECK constraint in the schema rejects
// any value not declared here.
type Status string

// Run states. Succeeded, Failed, Cancelled and TimedOut are terminal.
const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
	StatusTimedOut  Status = "timed_out"
)

// Terminal reports whether the status is final. A terminal run releases its
// lock key and never changes again.
func (s Status) Terminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusCancelled, StatusTimedOut:
		return true
	default:
		return false
	}
}

// Valid reports whether s is a known status.
func (s Status) Valid() bool {
	switch s {
	case StatusQueued, StatusRunning, StatusSucceeded, StatusFailed, StatusCancelled, StatusTimedOut:
		return true
	default:
		return false
	}
}

// ErrRunNotFound is returned when no run matches an ID.
var ErrRunNotFound = errors.New("run not found")

// ErrLockHeld is returned by CreateRun when a queued or running run already
// holds the new run's lock key.
var ErrLockHeld = errors.New("lock held by an active run")

// ErrRecoveryRequired means only explicit operator recovery can release the key.
var ErrRecoveryRequired = errors.New("run requires operator recovery")

// RecoveryState distinguishes a held interrupted run from an operator release.
type RecoveryState string

const (
	RecoveryNone     RecoveryState = "none"
	RecoveryRequired RecoveryState = "required"
	RecoveryReleased RecoveryState = "released"
)

// HiddenTarget is the target of a run that must not say what it acts on, such
// as the inject of an incident a learner is to find without being told.
const HiddenTarget = "(hidden)"

// Run is the durable record of one shelled-out operation.
type Run struct {
	ID       string
	Kind     string // platform.install, scenario.activate, …
	Target   string // what it acts on
	LockKey  string // exclusive key (ADR-0004); empty means no lock
	Status   Status
	Actor    string
	Script   string
	Argv     []string // argv-only - never a shell string (ADR-0003)
	Timeout  time.Duration
	QueuedAt time.Time

	StartedAt      time.Time
	EndedAt        time.Time
	Duration       time.Duration // measured monotonically, not EndedAt-StartedAt
	ExitCode       *int
	Error          string
	OwnerID        string
	RecoveryState  RecoveryState
	RecoveryReason string
	ReleasedAt     time.Time
	ReleasedBy     string
}

// Hidden reports whether the run's output and steps are withheld: they would
// name what its target hides.
func (r Run) Hidden() bool { return r.Target == HiddenTarget }

// CreateRun inserts a queued run. The caller supplies the ID so it can hand
// it back (for example in an HTTP 202) before the run starts.
func (s *Store) CreateRun(ctx context.Context, r Run) error {
	if r.ID == "" {
		return errors.New("store: run ID is required")
	}
	if r.Kind == "" {
		return errors.New("store: run kind is required")
	}
	if r.Status == "" {
		r.Status = StatusQueued
	}
	if !r.Status.Valid() {
		return fmt.Errorf("store: invalid run status %q", r.Status)
	}
	if r.QueuedAt.IsZero() {
		r.QueuedAt = s.now()
	}

	argv, err := json.Marshal(nonNil(r.Argv))
	if err != nil {
		return fmt.Errorf("encoding argv: %w", err)
	}

	// An active run takes its lock in the same statement that checks it, so
	// two writers, in this process or another, can never both hold one.
	guarded := r.LockKey != "" && (r.Status == StatusQueued || r.Status == StatusRunning)
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO runs (id, kind, target, lock_key, status, actor, argv, script,
		                  timeout_ms, queued_at, owner_id)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		WHERE NOT ? OR NOT EXISTS (
			SELECT 1 FROM runs WHERE lock_key = ? AND status IN (?, ?))`,
		r.ID, r.Kind, r.Target, r.LockKey, string(r.Status), r.Actor, string(argv),
		r.Script, r.Timeout.Milliseconds(), r.QueuedAt.UnixMicro(), r.OwnerID,
		guarded, r.LockKey, StatusQueued, StatusRunning)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("store: run %q already exists", r.ID)
		}
		return fmt.Errorf("creating run %s: %w", r.ID, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("creating run %s: %w", r.ID, err)
	} else if n == 0 {
		return fmt.Errorf("creating run %s: %w: %s", r.ID, ErrLockHeld, r.LockKey)
	}
	return nil
}

// StartRun marks a queued run as running. It returns an error, and changes
// nothing, if the run is no longer queued, so a run can start only once.
func (s *Store) StartRun(ctx context.Context, id string, at time.Time) error {
	return s.startRun(ctx, id, "", false, at)
}

// StartOwnedRun starts queued work only for its owner while recovery is clear.
func (s *Store) StartOwnedRun(ctx context.Context, id, owner string, at time.Time) error {
	if owner == "" {
		return errors.New("starting an owned run requires its owner ID")
	}
	return s.startRun(ctx, id, owner, true, at)
}

func (s *Store) startRun(ctx context.Context, id, owner string, owned bool, at time.Time) error {
	if at.IsZero() {
		at = s.now()
	}
	query := `UPDATE runs SET status = ?, started_at = ? WHERE id = ? AND status = ? AND recovery_state = ?`
	args := []any{StatusRunning, at.UnixMicro(), id, StatusQueued, RecoveryNone}
	if owned {
		query += ` AND owner_id = ?`
		args = append(args, owner)
	}
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("starting run %s: %w", id, err)
	}
	return expectOneRow(res, id, "run is not queued")
}

// FinishRun records a terminal state. exitCode may be nil when the run never
// produced one (cancelled before exec, or killed).
func (s *Store) FinishRun(ctx context.Context, id string, status Status, exitCode *int, runErr string, at time.Time, dur time.Duration) error {
	return s.finishRun(ctx, id, "", false, status, exitCode, runErr, at, dur)
}

// FinishOwnedRun records completion only for the current unrecovered owner.
func (s *Store) FinishOwnedRun(ctx context.Context, id, owner string, status Status, exitCode *int, runErr string, at time.Time, dur time.Duration) error {
	if owner == "" {
		return errors.New("finishing an owned run requires its owner ID")
	}
	return s.finishRun(ctx, id, owner, true, status, exitCode, runErr, at, dur)
}

func (s *Store) finishRun(ctx context.Context, id, owner string, owned bool, status Status, exitCode *int, runErr string, at time.Time, dur time.Duration) error {
	if !status.Terminal() {
		return fmt.Errorf("store: %q is not a terminal status", status)
	}
	if at.IsZero() {
		at = s.now()
	}

	var code sql.NullInt64
	if exitCode != nil {
		code = sql.NullInt64{Int64: int64(*exitCode), Valid: true}
	}
	// Stored in microseconds so a sub-millisecond run does not read as zero.
	var durUs sql.NullInt64
	if dur > 0 {
		durUs = sql.NullInt64{Int64: dur.Microseconds(), Valid: true}
	}

	// Only update a run that is not already terminal, so whichever of a
	// cancel and a normal exit lands first wins.
	query := `
		UPDATE runs
		   SET status = ?, exit_code = ?, error = ?, ended_at = ?, duration_us = ?
		 WHERE id = ? AND status IN (?, ?) AND recovery_state = ?`
	args := []any{string(status), code, runErr, at.UnixMicro(), durUs, id, StatusQueued, StatusRunning, RecoveryNone}
	if owned {
		query += ` AND owner_id = ?`
		args = append(args, owner)
	}
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("finishing run %s: %w", id, err)
	}
	return expectOneRow(res, id, "run already finished")
}

// GetRun loads one run.
func (s *Store) GetRun(ctx context.Context, id string) (Run, error) {
	row := s.db.QueryRowContext(ctx, selectRunColumns+` WHERE id = ?`, id)
	r, err := scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, fmt.Errorf("%w: %s", ErrRunNotFound, id)
	}
	return r, err
}

// RunFilter narrows ListRuns. The zero value lists everything, newest first.
type RunFilter struct {
	Status []Status
	Kind   string
	// Before returns only runs queued strictly before this instant; pass the
	// oldest QueuedAt from the previous page to get the next one.
	Before time.Time
	Limit  int
}

// ListRuns returns runs newest-first.
func (s *Store) ListRuns(ctx context.Context, f RunFilter) ([]Run, error) {
	var (
		where []string
		args  []any
	)
	if len(f.Status) > 0 {
		placeholders := make([]string, len(f.Status))
		for i, st := range f.Status {
			placeholders[i] = "?"
			args = append(args, string(st))
		}
		where = append(where, "status IN ("+strings.Join(placeholders, ",")+")")
	}
	if f.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, f.Kind)
	}
	if !f.Before.IsZero() {
		where = append(where, "queued_at < ?")
		args = append(args, f.Before.UnixMicro())
	}

	query := selectRunColumns
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY queued_at DESC, id DESC"

	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	query += " LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing runs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ActiveRunForLock returns the queued or running run that holds lockKey, if
// any. The run engine uses it to refuse conflicting work (ADR-0004).
func (s *Store) ActiveRunForLock(ctx context.Context, lockKey string) (Run, bool, error) {
	if lockKey == "" {
		return Run{}, false, nil
	}
	row := s.db.QueryRowContext(ctx,
		selectRunColumns+` WHERE lock_key = ? AND status IN (?, ?) ORDER BY queued_at ASC LIMIT 1`,
		lockKey, StatusQueued, StatusRunning)
	r, err := scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, err
	}
	return r, true, nil
}

// LastRunForLock returns the most recent run, in any status, that held
// lockKey. Services use it to answer "what last happened to this thing?",
// such as whether a component is installed.
func (s *Store) LastRunForLock(ctx context.Context, lockKey string) (Run, bool, error) {
	if lockKey == "" {
		return Run{}, false, nil
	}
	row := s.db.QueryRowContext(ctx,
		selectRunColumns+` WHERE lock_key = ? ORDER BY queued_at DESC, id DESC LIMIT 1`,
		lockKey)
	r, err := scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, err
	}
	return r, true, nil
}

// RecoverOrphanedRuns reconciles active rows only while their owner lock is held.
// Live owners keep their work; interrupted running work keeps its key.
func (s *Store) RecoverOrphanedRuns(ctx context.Context, at time.Time) ([]string, error) {
	if at.IsZero() {
		at = s.now()
	}
	owners, err := s.ActiveOwners(ctx)
	if err != nil {
		return nil, err
	}
	return s.recoverOwners(ctx, owners, at)
}

// PruneRuns deletes finished runs that ended before the given time, along with
// their logs and steps, and returns how many runs it deleted.
func (s *Store) PruneRuns(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM runs
		 WHERE ended_at IS NOT NULL AND ended_at < ?
		   AND status NOT IN (?, ?)`,
		before.UnixMicro(), StatusQueued, StatusRunning)
	if err != nil {
		return 0, fmt.Errorf("pruning runs: %w", err)
	}
	return res.RowsAffected()
}

const selectRunColumns = `
	SELECT id, kind, target, lock_key, status, actor, argv, script, timeout_ms,
	       queued_at, started_at, ended_at, duration_us, exit_code, error,
	       owner_id, recovery_state, recovery_reason, released_at, released_by
	  FROM runs`

// scanner covers both *sql.Row and *sql.Rows.
type scanner interface{ Scan(dest ...any) error }

func scanRun(sc scanner) (Run, error) {
	var (
		r         Run
		status    string
		argv      string
		timeoutMs int64
		queuedAt  int64
		started   sql.NullInt64
		ended     sql.NullInt64
		durUs     sql.NullInt64
		exitCode  sql.NullInt64
		released  sql.NullInt64
	)
	if err := sc.Scan(&r.ID, &r.Kind, &r.Target, &r.LockKey, &status, &r.Actor,
		&argv, &r.Script, &timeoutMs, &queuedAt, &started, &ended, &durUs,
		&exitCode, &r.Error, &r.OwnerID, &r.RecoveryState, &r.RecoveryReason, &released, &r.ReleasedBy); err != nil {
		return Run{}, err
	}

	r.Status = Status(status)
	r.Timeout = time.Duration(timeoutMs) * time.Millisecond
	r.QueuedAt = time.UnixMicro(queuedAt).UTC()
	r.StartedAt = fromMicro(started)
	r.EndedAt = fromMicro(ended)
	r.ReleasedAt = fromMicro(released)
	if durUs.Valid {
		r.Duration = time.Duration(durUs.Int64) * time.Microsecond
	}
	if exitCode.Valid {
		code := int(exitCode.Int64)
		r.ExitCode = &code
	}
	if err := json.Unmarshal([]byte(argv), &r.Argv); err != nil {
		return Run{}, fmt.Errorf("run %s: decoding argv: %w", r.ID, err)
	}
	return r, nil
}

func expectOneRow(res sql.Result, id, reason string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: %s (%s)", ErrRunNotFound, id, reason)
	}
	return nil
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
