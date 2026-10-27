// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sagar2395/snowopslabs/internal/runowner"
)

// ActiveOwners returns the distinct owners of queued and running work.
func (s *Store) ActiveOwners(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT owner_id FROM runs
		WHERE status IN (?, ?) ORDER BY owner_id`, StatusQueued, StatusRunning)
	if err != nil {
		return nil, fmt.Errorf("reading run owners: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var owners []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		owners = append(owners, id)
	}
	return owners, rows.Err()
}

func (s *Store) recoverOwners(ctx context.Context, owners []string, at time.Time) ([]string, error) {
	var changed []string
	for _, id := range owners {
		var lock *runowner.Lock
		if id != "" {
			var err error
			lock, err = runowner.Probe(ctx, s.path, id)
			if errors.Is(err, runowner.ErrBusy) {
				continue
			}
			if err != nil {
				return changed, fmt.Errorf("checking owner %s; repair its owner file before recovery: %w", id, err)
			}
		}
		ids, err := s.recoverOwner(ctx, id, at)
		closeErr := lock.Close()
		if err != nil || closeErr != nil {
			return changed, errors.Join(err, closeErr)
		}
		changed = append(changed, ids...)
	}
	return changed, nil
}

func (s *Store) recoverOwner(ctx context.Context, owner string, at time.Time) ([]string, error) {
	var changed []string
	err := s.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, status FROM runs
			WHERE owner_id = ? AND recovery_state = ? AND status IN (?, ?)`,
			owner, RecoveryNone, StatusQueued, StatusRunning)
		if err != nil {
			return err
		}
		type pendingRun struct {
			id     string
			status Status
		}
		var pending []pendingRun
		for rows.Next() {
			var row pendingRun
			if err := rows.Scan(&row.id, &row.status); err != nil {
				_ = rows.Close()
				return err
			}
			pending = append(pending, row)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, row := range pending {
			state, reason := RecoveryRequired, "owner_interrupted"
			status := row.status
			var ended sql.NullInt64
			message := "Recovery required: check the lab, then use labctl runs recover " + row.id + " --force"
			if owner == "" {
				reason = "legacy_upgrade"
			} else if row.status == StatusQueued {
				state, reason, status = RecoveryNone, "owner_exited_before_start", StatusCancelled
				ended = micro(at)
				message = "interrupted before execution: queued work cancelled after its owner exited"
			}
			res, err := tx.ExecContext(ctx, `UPDATE runs SET status = ?, recovery_state = ?,
				recovery_reason = ?, ended_at = ?, error = ?
				WHERE id = ? AND owner_id = ? AND recovery_state = ? AND status = ?`,
				status, state, reason, ended, message, row.id, owner, RecoveryNone, row.status)
			if err != nil {
				return err
			}
			if err := expectOneRow(res, row.id, "run changed during recovery"); err != nil {
				return err
			}
			if err := appendRecoveryLog(ctx, tx, row.id, message, at); err != nil {
				return err
			}
			changed = append(changed, row.id)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("recovering owner %s: %w", owner, err)
	}
	return changed, nil
}

func appendRecoveryLog(ctx context.Context, tx *sql.Tx, id, message string, at time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO run_logs (run_id, seq, ts, stream, text)
		SELECT ?, COALESCE(MAX(seq), 0) + 1, ?, ?, ? FROM run_logs WHERE run_id = ?`,
		id, at.UnixMicro(), StreamSystem, message, id)
	return err
}

// ReleaseRecoveredRun records an explicit operator override while holding the owner lock.
// Repeated releases return the existing record without changing its audit fields.
func (s *Store) ReleaseRecoveredRun(ctx context.Context, id, actor string) (Run, error) {
	if actor == "" {
		return Run{}, errors.New("operator recovery requires a local actor identity")
	}
	observed, err := s.GetRun(ctx, id)
	if err != nil {
		return Run{}, err
	}
	if observed.RecoveryState == RecoveryReleased {
		return observed, nil
	}
	if observed.RecoveryState != RecoveryRequired {
		return Run{}, fmt.Errorf("run %s does not require recovery; inspect it with labctl runs list", id)
	}
	if observed.OwnerID != "" {
		lock, err := runowner.Probe(ctx, s.path, observed.OwnerID)
		if err != nil {
			return Run{}, fmt.Errorf("cannot release run %s; stop its owner or repair its owner file: %w", id, err)
		}
		defer func() { _ = lock.Close() }()
	}
	var result Run
	err = s.tx(ctx, func(tx *sql.Tx) error {
		current, err := scanRun(tx.QueryRowContext(ctx, selectRunColumns+` WHERE id = ?`, id))
		if err != nil {
			return err
		}
		if current.RecoveryState == RecoveryReleased {
			result = current
			return nil
		}
		if current.OwnerID != observed.OwnerID || current.RecoveryState != RecoveryRequired || current.Status.Terminal() {
			return fmt.Errorf("run %s changed; inspect it again before recovery", id)
		}
		at := s.now().UTC().Truncate(time.Microsecond)
		message := "Key released by operator. The operation may still be running."
		res, err := tx.ExecContext(ctx, `UPDATE runs SET status = ?, recovery_state = ?,
			recovery_reason = 'forced_release', released_at = ?, released_by = ?, ended_at = ?, error = ?
			WHERE id = ? AND owner_id = ? AND recovery_state = ? AND status IN (?, ?)`,
			StatusCancelled, RecoveryReleased, at.UnixMicro(), actor, at.UnixMicro(), message,
			id, observed.OwnerID, RecoveryRequired, StatusQueued, StatusRunning)
		if err != nil {
			return err
		}
		if err := expectOneRow(res, id, "run changed during recovery"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit (ts, actor, action, target, run_id, source, detail)
			VALUES (?, ?, 'run.recover', ?, ?, 'cli', ?)`,
			at.UnixMicro(), actor, current.LockKey, id, "forced_release; former_owner="+current.OwnerID); err != nil {
			return fmt.Errorf("saving recovery audit; the key remains reserved: %w", err)
		}
		if err := appendRecoveryLog(ctx, tx, id, message+" Released by "+actor, at); err != nil {
			return err
		}
		result = current
		result.Status, result.RecoveryState, result.RecoveryReason = StatusCancelled, RecoveryReleased, "forced_release"
		result.ReleasedAt, result.ReleasedBy, result.EndedAt, result.Error = at, actor, at, message
		return nil
	})
	return result, err
}

func (s *Store) lockMigrationOwners(ctx context.Context) ([]*runowner.Lock, error) {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(runs)`)
	if err != nil {
		return nil, fmt.Errorf("reading migration owner fields: %w", err)
	}
	hasOwner := false
	for rows.Next() {
		var index, required, primary int
		var name, kind string
		var defaultValue sql.NullString
		if err := rows.Scan(&index, &name, &kind, &required, &defaultValue, &primary); err != nil {
			_ = rows.Close()
			return nil, err
		}
		hasOwner = hasOwner || name == "owner_id"
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	if !hasOwner {
		return nil, nil
	}
	ids, err := s.ActiveOwners(ctx)
	if err != nil {
		return nil, err
	}
	var held []*runowner.Lock
	for _, id := range ids {
		if id == "" {
			continue
		}
		lock, err := runowner.Probe(ctx, s.path, id)
		if err != nil {
			for _, acquired := range held {
				_ = acquired.Close()
			}
			return nil, fmt.Errorf("migration cannot check owner %s; stop all labctl processes and repair its owner file: %w", id, err)
		}
		held = append(held, lock)
	}
	return held, nil
}
