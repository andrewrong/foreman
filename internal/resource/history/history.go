// Package history writes hourly resource consumption to global.db
// resource_usage (resources.yaml RC-4); the web resources page reads it.
package history

import (
	"context"
	"database/sql"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/store/sqlite"
)

// Retention is how long rows are kept.
const Retention = 90 * 24 * time.Hour

// Row is one resource_usage row.
type Row struct {
	Hour        time.Time
	Resource    string
	CallerStage string
	Project     string
	Consumed    int64
	Capacity    int64
}

// History records and reads usage.
type History struct {
	db  *sqlite.DB // global.db
	clk clock.Clock
}

// New returns a History over global.db.
func New(db *sqlite.DB, clk clock.Clock) *History { return &History{db: db, clk: clk} }

// Record adds consumed to the hour bucket containing at; capacity is
// overwritten with the latest value. Old rows are pruned in the same
// transaction.
func (h *History) Record(ctx context.Context, at time.Time, resource, stage, project string, consumed, capacity int64) error {
	hour := at.Truncate(time.Hour).Unix()
	cutoff := h.clk.Now().Add(-Retention).Unix()
	return h.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO resource_usage(hour, resource, caller_stage, project, consumed, capacity)
VALUES (?,?,?,?,?,?)
ON CONFLICT(hour, resource, caller_stage, project)
DO UPDATE SET consumed = consumed + excluded.consumed, capacity = excluded.capacity`,
			hour, resource, stage, project, consumed, capacity); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM resource_usage WHERE hour < ?`, cutoff)
		return err
	})
}

// Query returns rows with from <= hour < to for a resource ("" = all),
// ordered by hour.
func (h *History) Query(ctx context.Context, resource string, from, to time.Time) ([]Row, error) {
	var out []Row
	err := h.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT hour, resource, caller_stage, project, consumed, capacity
FROM resource_usage WHERE hour >= ? AND hour < ? AND (? = '' OR resource = ?)
ORDER BY hour, resource, caller_stage, project`, from.Unix(), to.Unix(), resource, resource)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r Row
			var hr int64
			if err := rows.Scan(&hr, &r.Resource, &r.CallerStage, &r.Project, &r.Consumed, &r.Capacity); err != nil {
				return err
			}
			r.Hour = time.Unix(hr, 0).UTC()
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}
