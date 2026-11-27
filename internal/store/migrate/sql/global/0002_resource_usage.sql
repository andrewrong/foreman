-- resources.yaml RC-4: hourly resource consumption attributed to caller stage
-- and project. Retained 90 days (pruned by internal/resource/history).
CREATE TABLE resource_usage (
    hour         INTEGER NOT NULL,  -- unix seconds, truncated to the hour
    resource     TEXT    NOT NULL,
    caller_stage TEXT    NOT NULL,
    project      TEXT    NOT NULL,
    consumed     INTEGER NOT NULL,
    capacity     INTEGER NOT NULL,
    PRIMARY KEY (hour, resource, caller_stage, project)
) WITHOUT ROWID;
