package api

import (
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// Files a session may write in its run directory (D-002: AI output is only
// structured result files). The .tmp names are the PP-2 atomic-write staging
// files.
var sessionWritable = map[string]bool{
	"result.json": true, "result.json.tmp": true,
	"todo.json": true, "todo.json.tmp": true,
}

// Files foremand itself keeps in the run directory.
var foremandOwned = map[string]bool{
	"context.md": true, "todo.last-valid.json": true,
	"stdout.log": true, "stderr.log": true,
}

// StrayOutputs lists entries of the run directory that neither the session
// may write nor foremand owns. A non-empty result means the session wrote
// outside the protocol and the run must be rejected.
func StrayOutputs(runDir fs.FS) ([]string, error) {
	ents, err := fs.ReadDir(runDir, ".")
	if err != nil {
		return nil, err
	}
	var stray []string
	for _, e := range ents {
		if n := e.Name(); !sessionWritable[n] && !foremandOwned[n] {
			stray = append(stray, n)
		}
	}
	sort.Strings(stray)
	return stray, nil
}

// CheckOutputs returns an error naming stray run-directory entries.
func CheckOutputs(runDir fs.FS) error {
	stray, err := StrayOutputs(runDir)
	if err != nil {
		return err
	}
	if len(stray) > 0 {
		return fmt.Errorf("session wrote outside result files: %s", strings.Join(stray, ", "))
	}
	return nil
}
