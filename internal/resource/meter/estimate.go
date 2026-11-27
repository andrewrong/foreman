package meter

import (
	"math"
	"sort"
	"sync"
)

// Tokens is a run's token usage.
type Tokens struct{ Input, Output, CacheRead int64 }

// Weighted applies the configured weights.
func (t Tokens) Weighted(w TokenWeights) float64 {
	return float64(t.Input)*w.Input + float64(t.Output)*w.Output + float64(t.CacheRead)*w.CacheRead
}

const maxKSamples = 100

// Estimator computes the model reservation in percentage points
// (RES-MODEL reservation_estimate): p75 of the role's recent weighted tokens
// times k_model. In-process only.
type Estimator struct {
	cfg  Config
	mu   sync.Mutex
	runs map[string][]float64 // role -> weighted tokens, newest last
	k    map[string][]float64 // model -> points per weighted token
}

// NewEstimator returns an Estimator using cfg.
func NewEstimator(cfg Config) *Estimator {
	return &Estimator{cfg: cfg, runs: map[string][]float64{}, k: map[string][]float64{}}
}

// RecordRun records the token usage of a finished run of role.
func (e *Estimator) RecordRun(role string, t Tokens) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r := append(e.runs[role], t.Weighted(e.cfg.Weights))
	if n := e.cfg.EstimateRuns; n > 0 && len(r) > n {
		r = r[len(r)-n:]
	}
	e.runs[role] = r
}

// RecordDelta records one k_model sample: the utilization increase in points
// between adjacent readings and the tokens consumed in between. Samples with
// delta <= 0 (rolling-window decay) or no tokens are discarded.
func (e *Estimator) RecordDelta(model string, t Tokens, deltaPoints float64) {
	w := t.Weighted(e.cfg.Weights)
	if deltaPoints <= 0 || w <= 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	k := append(e.k[model], deltaPoints/w)
	if len(k) > maxKSamples {
		k = k[len(k)-maxKSamples:]
	}
	e.k[model] = k
}

// Estimate returns the reservation in points for a run of role on model.
// With fewer than EstimateMinSamples k samples (or no runs) it returns
// FallbackReservePoints.
func (e *Estimator) Estimate(role, model string) int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	ks, runs := e.k[model], e.runs[role]
	if len(ks) < e.cfg.EstimateMinSamples || len(runs) == 0 {
		return e.cfg.FallbackReservePoints
	}
	var sum float64
	for _, v := range ks {
		sum += v
	}
	sorted := append([]float64(nil), runs...)
	sort.Float64s(sorted)
	p75 := sorted[int(math.Ceil(0.75*float64(len(sorted))))-1]
	est := int64(math.Ceil(p75*sum/float64(len(ks)) - 1e-9))
	if est < 1 {
		est = 1
	}
	return est
}
