-- forge.yaml FG-3: ETag cache of conditional GETs. Bodies over 256 KiB are
-- never stored; the table is capped (64 MiB) and evicted by fetched_at.
CREATE TABLE forge_etags (
    url        TEXT    PRIMARY KEY,
    etag       TEXT    NOT NULL,
    body       BLOB    NOT NULL,
    fetched_at INTEGER NOT NULL   -- unix milliseconds
) WITHOUT ROWID;
CREATE INDEX forge_etags_fetched_at ON forge_etags (fetched_at);
