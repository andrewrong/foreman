-- work.db entity tables (spec/design/entities.yaml, db: work.db).
-- Conventions: times are RFC 3339 UTC text; lists and maps are JSON text;
-- booleans are 0/1. Only internal/pipeline/state writes steps.state/stage/
-- reason/resume_stage and requirements.hold (C-01).

-- Sequences for ids that are numbered per scope (run r<base36>, step S<n>,
-- finding F<n>, discovery X<n>, ask Q-<project>-<n>).
CREATE TABLE id_seq (
    name TEXT PRIMARY KEY,
    next INTEGER NOT NULL
) WITHOUT ROWID;

-- E-REQ (+ E-SPEC embedded as JSON)
CREATE TABLE requirements (
    id                TEXT PRIMARY KEY,
    kind              TEXT NOT NULL CHECK (kind IN ('feature', 'bug', 'chore')),
    source            TEXT NOT NULL CHECK (source IN ('owner', 'nightly', 'prod', 'improve')),
    priority          TEXT NOT NULL CHECK (priority IN ('P0', 'P1', 'P2', 'P3')),
    spec_version      INTEGER NOT NULL CHECK (spec_version >= 1),
    spec              TEXT NOT NULL CHECK (json_valid(spec)),
    branch            TEXT NOT NULL,
    parked            INTEGER NOT NULL DEFAULT 0 CHECK (parked IN (0, 1)),
    hold              TEXT NOT NULL DEFAULT 'none'
                      CHECK (hold IN ('none', 'replan', 'rescue', 'owner', 'parked')),
    -- deadline of the current owner/parked hold (ask deadline, 7d reminders)
    hold_deadline     TEXT,
    -- low-priority triggers that arrived while a higher hold was active:
    -- [{trigger, h_item, subject, cause_counter, detail}]
    pending_triggers  TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(pending_triggers)),
    -- {replans, planner_failures, req_rescues, fix_steps, access_requests}
    req_counters      TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(req_counters)),
    last_l0_sha       TEXT,
    session_repo      TEXT,
    session_repo_state TEXT NOT NULL DEFAULT 'none'
                      CHECK (session_repo_state IN ('none', 'active', 'releasing', 'released')),
    pending_run       TEXT NOT NULL DEFAULT 'none' CHECK (pending_run IN ('none', 'planner', 'rescuer')),
    -- trigger reasons appended for the next planner / rescuer Run
    planner_input     TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(planner_input)),
    rescuer_input     TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(rescuer_input)),
    base_fix_sha      TEXT,
    dropped_reason    TEXT,
    status            TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'submitted')),
    size              TEXT NOT NULL CHECK (size IN ('s', 'm', 'l')),
    budget_tokens     INTEGER NOT NULL DEFAULT 0,
    budget_quota_pct  INTEGER NOT NULL DEFAULT 0,
    -- H4 resume appends one original budget each time; effective = budget * (1 + n)
    budget_extensions INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX requirements_hold ON requirements (hold) WHERE hold <> 'none';
CREATE INDEX requirements_pending_run ON requirements (pending_run) WHERE pending_run <> 'none';

-- Older spec versions (D-070): the version being replaced is saved here when
-- spec_version is bumped.
CREATE TABLE req_spec_versions (
    req_id  TEXT NOT NULL REFERENCES requirements (id),
    version INTEGER NOT NULL,
    spec    TEXT NOT NULL CHECK (json_valid(spec)),
    saved_at TEXT NOT NULL,
    PRIMARY KEY (req_id, version)
) WITHOUT ROWID;

-- E-STEP (+ E-CNT counters as a JSON map)
CREATE TABLE steps (
    id                TEXT PRIMARY KEY,           -- <req id>/S<n>
    req_id            TEXT NOT NULL REFERENCES requirements (id),
    seq               INTEGER NOT NULL,           -- the n of S<n>
    type              TEXT NOT NULL CHECK (type IN ('build', 'find_cause', 'fix', 'integrate')),
    code_role         TEXT NOT NULL CHECK (code_role IN ('coder', 'diagnoser')),
    goal              TEXT NOT NULL,
    acceptance        TEXT NOT NULL CHECK (json_valid(acceptance)),
    depends_on        TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(depends_on)),
    touches           TEXT NOT NULL CHECK (json_valid(touches)),
    state             TEXT NOT NULL CHECK (state IN ('queued', 'running', 'waiting', 'done', 'dropped')),
    stage             TEXT NOT NULL CHECK (stage IN ('code', 'check', 'review', 'merge')),
    -- required while waiting(hold); enforced by Transition (the effect that sets it runs after the state write)
    resume_stage      TEXT CHECK (resume_stage IN ('code', 'check', 'review', 'merge')),
    access_grants     TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(access_grants)),
    guidance          TEXT,
    denial            TEXT,
    -- waiting: deps|quota|infra|access|rescue|hold|ci|fix; dropped: free text
    reason            TEXT,
    deadline          TEXT,                       -- NULL for the none_bounded waits
    counters          TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(counters)),
    session_repo      TEXT,
    commit_sha        TEXT,
    last_reviewed_sha TEXT,
    fix_for           TEXT REFERENCES steps (id), -- fix step -> the integrate step it repairs
    UNIQUE (req_id, seq),
    CHECK (state NOT IN ('waiting', 'dropped') OR reason IS NOT NULL),
    CHECK (state <> 'waiting' OR reason IN ('deps', 'quota', 'infra', 'access', 'rescue', 'hold', 'ci', 'fix')),
    CHECK ((type = 'integrate' AND stage IN ('check', 'review', 'merge'))
        OR (type <> 'integrate' AND stage IN ('code', 'check', 'review')))
);
CREATE INDEX steps_req_state ON steps (req_id, state);
CREATE INDEX steps_state_deadline ON steps (state, deadline);

-- E-PR (v1: one pull request per requirement, D-042)
CREATE TABLE pull_requests (
    req_id                 TEXT PRIMARY KEY REFERENCES requirements (id),
    number                 INTEGER,
    head_branch            TEXT NOT NULL,
    head_sha               TEXT,
    state                  TEXT NOT NULL DEFAULT 'unopened'
                           CHECK (state IN ('unopened', 'open', 'merged', 'closed')),
    last_reviewed_sha      TEXT,
    guardrail_approved_sha TEXT,
    l1_reruns              TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(l1_reruns))  -- head sha -> count
) WITHOUT ROWID;

-- E-RUN. status is the reconciler handshake (pending -> running -> ended,
-- killing -> killed); the identity columns never change after insert.
CREATE TABLE runs (
    id              TEXT PRIMARY KEY,             -- r<base36 seq>, D-070
    req_id          TEXT NOT NULL REFERENCES requirements (id),
    step_id         TEXT REFERENCES steps (id),   -- NULL for requirement-scoped runs
    role            TEXT NOT NULL CHECK (role IN
                    ('intake', 'planner', 'coder', 'reviewer', 'rescuer', 'diagnoser', 'improver',
                     'check', 'ci_watch', 'merge')),
    status          TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'running', 'killing', 'killed', 'ended')),
    model           TEXT,
    scope           TEXT CHECK (scope IN ('step', 'requirement')),
    spec_version    INTEGER NOT NULL,
    snapshot_id     TEXT,
    changed_outcome INTEGER CHECK (changed_outcome IN (0, 1)),
    harness         TEXT,
    pid             INTEGER,
    pid_start       TEXT,
    dir             TEXT NOT NULL,
    config_rev      INTEGER NOT NULL,
    prompt_hash     TEXT,
    reserved        TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(reserved)),
    input           TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(input)),  -- trigger reasons (planner, rescuer)
    started_at      TEXT NOT NULL,
    ended_at        TEXT,
    outcome         TEXT CHECK (outcome IN ('ok', 'check_failed', 'review_changes', 'spec_conflict',
                                            'infra', 'bad_result', 'budget_exceeded', 'access_request')),
    usage           TEXT CHECK (usage IS NULL OR json_valid(usage)),
    CHECK (role <> 'rescuer' OR scope IS NOT NULL),
    CHECK (role <> 'planner' OR snapshot_id IS NOT NULL)
) WITHOUT ROWID;
CREATE INDEX runs_step_status ON runs (step_id, status);
CREATE INDEX runs_req_status ON runs (req_id, status);

-- Planner input snapshot of the steps (GEM-4); referenced by runs.snapshot_id.
CREATE TABLE plan_snapshots (
    id         TEXT PRIMARY KEY,
    req_id     TEXT NOT NULL REFERENCES requirements (id),
    created_at TEXT NOT NULL,
    steps      TEXT NOT NULL CHECK (json_valid(steps))
) WITHOUT ROWID;

-- E-FIND
CREATE TABLE findings (
    id            TEXT PRIMARY KEY,               -- F<n>
    req_id        TEXT NOT NULL REFERENCES requirements (id),
    kind          TEXT NOT NULL CHECK (kind IN ('review', 'check')),
    scope         TEXT NOT NULL,                  -- step id or req id
    severity      TEXT NOT NULL CHECK (severity IN ('blocking', 'minor')),
    location      TEXT CHECK (location IS NULL OR json_valid(location)),       -- {path, line}
    failing_tests TEXT CHECK (failing_tests IS NULL OR json_valid(failing_tests)),
    log_tail      TEXT CHECK (log_tail IS NULL OR length(log_tail) <= 8000),
    problem       TEXT NOT NULL,
    close_when    TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed', 'rejected')),
    round         INTEGER NOT NULL,
    CHECK (kind <> 'review' OR location IS NOT NULL),
    CHECK (kind <> 'check' OR (failing_tests IS NOT NULL AND log_tail IS NOT NULL))
) WITHOUT ROWID;
CREATE INDEX findings_scope ON findings (scope, kind, status);

-- E-DISC (append only)
CREATE TABLE discoveries (
    id         TEXT PRIMARY KEY,                  -- X<n>
    req_id     TEXT NOT NULL REFERENCES requirements (id),
    run_id     TEXT NOT NULL,
    kind       TEXT NOT NULL CHECK (kind IN
               ('answer', 'extra_file', 'spec_conflict', 'cause_proof', 'access_request', 'note')),
    ref        TEXT,
    text       TEXT NOT NULL CHECK (length(text) <= 500),
    evidence   TEXT CHECK (evidence IS NULL OR json_valid(evidence)),
    supersedes TEXT REFERENCES discoveries (id),
    CHECK (kind NOT IN ('answer', 'spec_conflict', 'extra_file') OR evidence IS NOT NULL)
) WITHOUT ROWID;
CREATE INDEX discoveries_req ON discoveries (req_id);
CREATE TRIGGER discoveries_no_update BEFORE UPDATE ON discoveries
BEGIN SELECT RAISE(ABORT, 'discoveries is append-only'); END;
CREATE TRIGGER discoveries_no_delete BEFORE DELETE ON discoveries
BEGIN SELECT RAISE(ABORT, 'discoveries is append-only'); END;

-- E-ASK
CREATE TABLE asks (
    id            TEXT PRIMARY KEY,               -- Q-<project>-<n>
    req_id        TEXT NOT NULL REFERENCES requirements (id),
    h_item        TEXT NOT NULL CHECK (h_item IN ('H1', 'H2', 'H3', 'H4', 'H5', 'H6', 'H7')),
    subject       TEXT NOT NULL,                  -- step id, req id or 'config'
    what          TEXT NOT NULL,
    changes       TEXT NOT NULL,
    checked       TEXT NOT NULL,
    options       TEXT NOT NULL CHECK (json_valid(options) AND json_array_length(options) BETWEEN 2 AND 3),
    recommended   TEXT NOT NULL,
    answer        TEXT,
    status        TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'answered', 'moot', 'expired')),
    -- counter(s) cleared once by H14 resume: comma-separated names, or 'budget' (H4)
    cause_counter TEXT,
    sent_at       TEXT NOT NULL,
    deadline      TEXT NOT NULL                   -- default sent_at + 72h
) WITHOUT ROWID;
CREATE INDEX asks_req_status ON asks (req_id, status);
CREATE INDEX asks_subject_status ON asks (subject, status);

CREATE VIEW v_open_asks AS SELECT * FROM asks WHERE status = 'open';

-- Queue consumed by the notify module (alert_starved, notify effects).
CREATE TABLE notify_queue (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    kind       TEXT NOT NULL,
    req_id     TEXT NOT NULL REFERENCES requirements (id),
    subject    TEXT NOT NULL,
    message    TEXT NOT NULL,
    created_at TEXT NOT NULL,
    sent_at    TEXT
);
CREATE INDEX notify_queue_subject ON notify_queue (kind, subject, created_at);
