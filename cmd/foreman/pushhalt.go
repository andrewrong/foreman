package main

import (
	"context"
	"database/sql"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/store/sqlite"
	"github.com/andrewrong/foreman/internal/workspace/integrate"
)

// pushHalts keeps the D-050 push-halt flag of one project in global.db.
type pushHalts struct {
	db      *sqlite.DB
	project string
	clock   clock.Clock
}

func (p pushHalts) LoadHalt(ctx context.Context) (integrate.HaltInfo, bool, error) {
	var h integrate.HaltInfo
	err := p.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT branch, before_sha, after_sha FROM push_halt WHERE project = ?`, p.project).
			Scan(&h.Branch, &h.Before, &h.After)
	})
	if err == sql.ErrNoRows {
		return h, false, nil
	}
	return h, err == nil, err
}

func (p pushHalts) SaveHalt(ctx context.Context, h integrate.HaltInfo) error {
	return p.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO push_halt (project, branch, before_sha, after_sha, halted_at) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (project) DO NOTHING`, p.project, h.Branch, h.Before, h.After, p.clock.Now().UTC().Format("2006-01-02T15:04:05.000Z"))
		return err
	})
}

func (p pushHalts) ClearHalt(ctx context.Context) error {
	return p.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM push_halt WHERE project = ?`, p.project)
		return err
	})
}
