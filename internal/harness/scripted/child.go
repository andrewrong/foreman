package scripted

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/andrewrong/foreman/internal/resource/meter"
)

// Script is the JSON the child executes in order.
type Script struct {
	Actions []Action `json:"actions"`
}

// Action is exactly one of its fields.
type Action struct {
	Say       string                        `json:"say,omitempty"`        // assistant text message
	Emit      json.RawMessage               `json:"emit,omitempty"`       // raw stream-json line
	RateLimit map[string]meter.ClaudeWindow `json:"rate_limit,omitempty"` // rate_limit_event windows
	Write     *Write                        `json:"write,omitempty"`
	Git       []string                      `json:"git,omitempty"` // git args, run in cwd
	Finish    *Finish                       `json:"finish,omitempty"`
	Exit      *int                          `json:"exit,omitempty"` // exit immediately with code
	// Block opens the named FIFO for reading and waits for end of file, so a
	// test holds a session at this point until it opens and closes the FIFO
	// for writing (no timers, no polling).
	Block string `json:"block,omitempty"`
}

// Write creates a file. Path is relative to the working directory; a
// "$RUN_DIR/" prefix means the run directory. Content or JSON gives the body;
// {{HEAD}} is replaced by the full sha of HEAD and {{RUN_ID}} by the run id.
// Writes are atomic (tmp + rename, PP-2) unless NonAtomic is set.
type Write struct {
	Path      string          `json:"path"`
	Content   string          `json:"content,omitempty"`
	JSON      json.RawMessage `json:"json,omitempty"`
	NonAtomic bool            `json:"non_atomic,omitempty"`
}

// Finish is the final result line.
type Finish struct {
	Turns   int     `json:"turns"`
	CostUSD float64 `json:"cost_usd"`
	Input   int64   `json:"input_tokens"`
	Output  int64   `json:"output_tokens"`
	Text    string  `json:"text"`
	IsError bool    `json:"is_error"`
}

func (a Action) validate() error {
	n := 0
	for _, set := range []bool{a.Say != "", len(a.Emit) > 0, len(a.RateLimit) > 0, a.Write != nil, len(a.Git) > 0, a.Finish != nil, a.Exit != nil, a.Block != ""} {
		if set {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("action must set exactly one field, got %d", n)
	}
	return nil
}

// MaybeRunChild runs the script and exits if this process was started in
// child mode; otherwise it returns immediately. Call it first in main or
// TestMain.
func MaybeRunChild() {
	if os.Getenv(EnvChild) != "1" {
		return
	}
	os.Exit(RunChild(os.Getenv, os.Stdout, os.Stderr))
}

// RunChild executes the script named by env and returns the exit code.
func RunChild(env func(string) string, stdout, stderr io.Writer) int {
	raw, err := os.ReadFile(env(EnvScript))
	if err != nil {
		fmt.Fprintln(stderr, "scripted:", err)
		return 2
	}
	var sc Script
	if err := json.Unmarshal(raw, &sc); err != nil {
		fmt.Fprintln(stderr, "scripted: bad script:", err)
		return 2
	}
	for i, a := range sc.Actions {
		if err := a.validate(); err != nil {
			fmt.Fprintf(stderr, "scripted: action %d: %v\n", i, err)
			return 2
		}
	}
	c := &child{env: env, out: stdout, errw: stderr}
	c.line(map[string]any{"type": "system", "subtype": "init", "session_id": "scripted-" + env(EnvRunID), "model": c.model(), "tools": []string{}})
	finished := false
	for i, a := range sc.Actions {
		code, stop, err := c.do(a)
		if err != nil {
			fmt.Fprintf(stderr, "scripted: action %d: %v\n", i, err)
			return 3
		}
		if a.Finish != nil {
			finished = true
		}
		if stop {
			return code
		}
	}
	if !finished {
		c.finish(&Finish{})
	}
	return 0
}

type child struct {
	env  func(string) string
	out  io.Writer
	errw io.Writer
}

// model is the model the Run was given (Invocation env), "scripted" if none.
func (c *child) model() string {
	if m := c.env(EnvModel); m != "" {
		return m
	}
	return "scripted"
}

func (c *child) line(v any) {
	b, _ := json.Marshal(v)
	c.out.Write(append(b, '\n'))
}

func (c *child) finish(f *Finish) {
	c.line(map[string]any{
		"type": "result", "subtype": "success", "is_error": f.IsError, "num_turns": f.Turns,
		"result": f.Text, "total_cost_usd": f.CostUSD, "terminal_reason": "completed",
		"usage":      map[string]int64{"input_tokens": f.Input, "output_tokens": f.Output},
		"modelUsage": map[string]any{"scripted": map[string]any{"inputTokens": f.Input, "outputTokens": f.Output, "costUSD": f.CostUSD}},
	})
}

func (c *child) do(a Action) (code int, stop bool, err error) {
	switch {
	case a.Say != "":
		c.line(map[string]any{"type": "assistant", "message": map[string]any{
			"model": "scripted", "content": []map[string]any{{"type": "text", "text": a.Say}}}})
	case len(a.Emit) > 0:
		c.out.Write(append(bytes.TrimSpace(a.Emit), '\n'))
	case len(a.RateLimit) > 0:
		c.line(map[string]any{"type": "rate_limit_event", "rate_limit_info": map[string]any{"unifiedWindows": a.RateLimit}})
	case a.Write != nil:
		err = c.write(a.Write)
	case len(a.Git) > 0:
		cmd := exec.Command("git", a.Git...)
		cmd.Stdout, cmd.Stderr = c.errw, c.errw
		err = cmd.Run()
	case a.Finish != nil:
		c.finish(a.Finish)
	case a.Exit != nil:
		return *a.Exit, true, nil
	case a.Block != "":
		f, oerr := os.Open(a.Block) // blocks until a writer opens the FIFO
		if oerr != nil {
			return 0, false, oerr
		}
		_, err = io.Copy(io.Discard, f)
		f.Close()
	}
	return 0, false, err
}

func (c *child) expand(s string) (string, error) {
	if strings.Contains(s, "{{HEAD}}") {
		out, err := exec.Command("git", "rev-parse", "HEAD").Output()
		if err != nil {
			return "", fmt.Errorf("{{HEAD}}: %w", err)
		}
		s = strings.ReplaceAll(s, "{{HEAD}}", strings.TrimSpace(string(out)))
	}
	return strings.ReplaceAll(s, "{{RUN_ID}}", c.env(EnvRunID)), nil
}

func (c *child) write(w *Write) error {
	path := w.Path
	if rest, ok := strings.CutPrefix(path, "$RUN_DIR/"); ok {
		path = filepath.Join(c.env(EnvRunDir), rest)
	}
	body := w.Content
	if len(w.JSON) > 0 {
		body = string(w.JSON)
	}
	body, err := c.expand(body)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if w.NonAtomic {
		return os.WriteFile(path, []byte(body), 0o644)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
