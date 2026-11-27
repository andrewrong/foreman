package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// IntegrityCheck runs PRAGMA integrity_check (D-047 step 2) and returns an error
// naming the first problem sqlite reports.
func (d *DB) IntegrityCheck(ctx context.Context) error {
	var res string
	if err := d.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&res)
	}); err != nil {
		return err
	}
	if res != "ok" {
		return fmt.Errorf("sqlite: %s: integrity_check: %s", d.path, res)
	}
	return nil
}

// SchemaVersion is the highest applied migration version.
func (d *DB) SchemaVersion(ctx context.Context) (int, error) {
	var v sql.NullInt64
	err := d.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&v)
	})
	return int(v.Int64), err
}
