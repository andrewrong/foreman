package code

import (
	"context"

	"github.com/andrewrong/foreman/internal/spec/model"
)

// Run executes the code stage of a work step (running(code)) and returns the
// event for state.Transition: run_ok, spec_conflict, infra or bad_result.
func Run(ctx context.Context, env Env, r Request) (model.Event, error) {
	res, err := env.RunSession(ctx, r)
	if err != nil {
		return model.Event{}, err
	}
	return EventOf(res), nil
}

// EventOf maps a finished coder session to its event.
//
// An access_request result is reported as bad_result in M1: the R1/R2/R3
// tiering (S78-S82) needs the project declaration, which is not wired yet.
func EventOf(res Result) model.Event {
	if res.Parsed == nil {
		return res.Event
	}
	ev := res.Event // carries the usage
	switch res.Parsed.Status {
	case "ok":
		ev.Name = model.EventRunOK
		if n := len(res.Parsed.Commits); n > 0 {
			ev.Head = res.Parsed.Commits[n-1]
		}
	case "spec_conflict":
		ev.Name, ev.Detail = model.EventSpecConflict, res.Parsed.Note
	default:
		ev.Name, ev.Detail = model.EventBadResult, "unsupported result status "+res.Parsed.Status
	}
	return ev
}
