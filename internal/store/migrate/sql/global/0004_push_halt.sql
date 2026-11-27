-- D-050: after a push moved the default branch, all pushes of the project stop until the
-- owner resolves H6. The flag survives restart; clearing it is an explicit owner action.
CREATE TABLE push_halt (
    project   TEXT PRIMARY KEY,
    branch    TEXT NOT NULL,
    before_sha TEXT NOT NULL,
    after_sha  TEXT NOT NULL,
    halted_at TEXT NOT NULL
) WITHOUT ROWID;
