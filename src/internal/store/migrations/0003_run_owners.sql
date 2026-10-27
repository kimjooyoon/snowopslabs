-- Keep interrupted work reserved until an operator records an explicit release.
ALTER TABLE runs ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN recovery_state TEXT NOT NULL DEFAULT 'none'
    CHECK (recovery_state IN ('none', 'required', 'released'));
ALTER TABLE runs ADD COLUMN recovery_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN released_at INTEGER;
ALTER TABLE runs ADD COLUMN released_by TEXT NOT NULL DEFAULT '';

UPDATE runs SET recovery_state = 'required', recovery_reason = 'legacy_upgrade'
WHERE status IN ('queued', 'running');
