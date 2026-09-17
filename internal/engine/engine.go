// Package engine compiles decisions into checks and runs them against a
// snapshot. It is pure: nothing here touches the filesystem, the network or an
// LLM. That property is what lets the same engine run in a CLI, an MCP server,
// a CI job and (later) a batch over hundreds of repos without modification.
package engine

import (
	"fmt"
	"runtime"
	"sort"
	"sync"

	"github.com/rohan/jasper/internal/glob"
	"github.com/rohan/jasper/internal/model"
)

// Check is seam 2 of 4. Compile validates configuration once, at load time, so
// a malformed decision file fails immediately with a file name rather than
// silently at check time. Run is then a closure over already-parsed config.
type Check interface {
	Kind() string
	Compile(cfg model.RawConfig, d *model.Decision) (Runner, error)
}

type Runner interface {
	Needs() model.Capability
	Run(*model.Snapshot) []model.Finding
}

var registry = map[string]Check{}

// Register is called from each check's init().
func Register(c Check) { registry[c.Kind()] = c }

func Kinds() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type compiled struct {
	decision *model.Decision
	kind     string
	runner   Runner
}

type Engine struct{ compiled []compiled }

// Compile turns a decision set into runnable checks. Decisions that are
// superseded or not yet accepted contribute nothing.
func Compile(decisions []*model.Decision) (*Engine, error) {
	e := &Engine{}
	for _, d := range decisions {
		if !d.Active() {
			continue
		}
		for _, rule := range d.Enforce {
			c, ok := registry[rule.Kind]
			if !ok {
				return nil, fmt.Errorf("%s: %s: unknown check %q (known: %v)",
					d.Path, d.ID, rule.Kind, Kinds())
			}
			r, err := c.Compile(rule.Cfg, d)
			if err != nil {
				return nil, fmt.Errorf("%s: %s: %s: %w", d.Path, d.ID, rule.Kind, err)
			}
			e.compiled = append(e.compiled, compiled{decision: d, kind: rule.Kind, runner: r})
		}
	}
	return e, nil
}

func (e *Engine) Len() int { return len(e.compiled) }

// Run executes every compiled check concurrently and returns findings in a
// stable order.
func (e *Engine) Run(snap *model.Snapshot) []model.Finding {
	var (
		mu  sync.Mutex
		all []model.Finding
		wg  sync.WaitGroup
	)
	sem := make(chan struct{}, runtime.NumCPU())

	for _, c := range e.compiled {
		wg.Add(1)
		sem <- struct{}{}
		go func(c compiled) {
			defer wg.Done()
			defer func() { <-sem }()

			found := c.runner.Run(snap)
			out := make([]model.Finding, 0, len(found))
			for _, f := range found {
				if scopedOut(c.decision, f) {
					continue
				}
				f.Decision = c.decision.ID
				f.Rule = c.kind
				if f.Severity == "" {
					f.Severity = model.SevError
				}
				out = append(out, f)
			}
			if len(out) == 0 {
				return
			}
			mu.Lock()
			all = append(all, out...)
			mu.Unlock()
		}(c)
	}
	wg.Wait()

	sort.Slice(all, func(i, j int) bool {
		if all[i].Decision != all[j].Decision {
			return all[i].Decision < all[j].Decision
		}
		if all[i].File != all[j].File {
			return all[i].File < all[j].File
		}
		return all[i].Line < all[j].Line
	})
	return all
}

// scopedOut applies a decision's grandfathering. Without this, enabling a rule
// on an existing repo produces hundreds of failures at once and the user turns
// Jasper off permanently.
func scopedOut(d *model.Decision, f model.Finding) bool {
	if d.Scope == nil {
		return false
	}
	return glob.MatchAny(d.Scope.Exclude, string(f.File))
}

// Report is the result of one check run.
type Report struct {
	Findings  []model.Finding `json:"findings"`
	Checks    int             `json:"checks"`
	Decisions int             `json:"decisions"`
	Files     int             `json:"files"`
	Imports   int             `json:"imports"`
	Cached    bool            `json:"cached"`
	ElapsedMS int64           `json:"elapsed_ms"`
}

func (r *Report) Errors() int {
	n := 0
	for _, f := range r.Findings {
		if f.Severity == model.SevError {
			n++
		}
	}
	return n
}

func (r *Report) OK() bool { return r.Errors() == 0 }
