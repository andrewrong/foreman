package sched

import (
	"database/sql"
	"encoding/json"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/andrewrong/foreman/internal/spec/model"
)

// D-091: unknown usage is NULL in runs.usage, known usage carries turns and
// cost next to the tokens.
func TestRunUsageUnknownIsNullNotZero(t *testing.T) {
	if v, err := usageJSON(model.Usage{}); err != nil || v != nil {
		t.Errorf("unknown usage = %v, %v; want nil", v, err)
	}
	v, err := usageJSON(model.Usage{Known: true, Input: 5, Output: 7, Turns: 3, CostUSD: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]float64
	if err := json.Unmarshal([]byte(v.(string)), &got); err != nil {
		t.Fatal(err)
	}
	if got["input_tokens"] != 5 || got["output_tokens"] != 7 || got["turns"] != 3 || got["cost_usd"] != 0.5 {
		t.Errorf("usage json %v", got)
	}
}

// The budget guard sums what endRun wrote: known usage counts, NULL (unknown)
// counts as nothing and does not break the sum.
func TestBudgetGuardSumsStoredUsage(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE runs (req_id TEXT, usage TEXT)`); err != nil {
		t.Fatal(err)
	}
	for _, u := range []model.Usage{{Known: true, Input: 100, Output: 40}, {Known: true, Input: 1, Output: 2}, {}} {
		v, err := usageJSON(u)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO runs VALUES ('r', ?)`, v); err != nil {
			t.Fatal(err)
		}
	}
	var sum int64
	if err := db.QueryRow(`SELECT ` + usageTokensSQL + ` FROM runs WHERE req_id = 'r'`).Scan(&sum); err != nil || sum != 143 {
		t.Errorf("summed usage = %d, %v; want 143", sum, err)
	}
}
