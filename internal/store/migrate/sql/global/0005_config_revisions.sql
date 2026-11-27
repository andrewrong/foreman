-- config.yaml / D-022: every saved change to the owner's config files is a
-- revision. snapshot is a JSON object {relative path: file content} of the
-- whole config directory after the change; rollback restores it. diff holds
-- changed lines only. Config files hold no secrets (D-026), so neither does
-- the table.
CREATE TABLE config_revisions (
    rev      INTEGER PRIMARY KEY AUTOINCREMENT,
    diff     TEXT NOT NULL,
    actor    TEXT NOT NULL,   -- owner | ai
    time     TEXT NOT NULL,   -- RFC 3339, UTC
    snapshot TEXT NOT NULL
);
-- actor=ai saves are proposals: stored, never applied until the owner accepts (H6).
CREATE TABLE config_proposals (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    file       TEXT NOT NULL,
    content    TEXT NOT NULL,
    diff       TEXT NOT NULL,
    actor      TEXT NOT NULL,
    time       TEXT NOT NULL,
    state      TEXT NOT NULL DEFAULT 'pending',   -- pending | accepted | rejected
    decided_at TEXT
);
