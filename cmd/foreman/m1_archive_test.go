package main

import (
	"io/fs"
	"os"
	"path/filepath"
)

// D-086 removes runs/<run id>/ once a Run's results are committed, so a
// scenario that inspects a Run's context.md or stdout.log afterwards cannot
// read them from runs/. archiveRuns copies the regular files of every run
// directory to <root>/archive/<run id>/ on each Transition, which happens
// before the directory is removed.
func (s *scenario) archiveRuns() {
	src := filepath.Join(s.f.home, "runs")
	dst := filepath.Join(filepath.Dir(s.f.home), "archive")
	_ = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		out := filepath.Join(dst, rel)
		if os.MkdirAll(filepath.Dir(out), 0o700) == nil {
			_ = os.WriteFile(out, b, 0o600)
		}
		return nil
	})
}

// archivePath is where archiveRuns put file of a Run.
func (s *scenario) archivePath(runID, file string) string {
	return filepath.Join(filepath.Dir(s.f.home), "archive", runID, file)
}
