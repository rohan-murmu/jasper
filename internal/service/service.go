// Package service is the orchestration layer. Every port (CLI today; MCP and
// LSP later) is a thin adapter over these methods and holds no logic of its
// own. That is what makes a new surface a day of work instead of a refactor.
package service

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rohan/jasper/internal/engine"
	"github.com/rohan/jasper/internal/facts"
	"github.com/rohan/jasper/internal/model"
	"github.com/rohan/jasper/internal/scan"
	"github.com/rohan/jasper/internal/store"

	// Languages and checks register themselves. Adding either is one new
	// package plus one line here.
	_ "github.com/rohan/jasper/internal/engine/checks"
	_ "github.com/rohan/jasper/internal/scan/lang/golang"
	_ "github.com/rohan/jasper/internal/scan/lang/python"
	_ "github.com/rohan/jasper/internal/scan/lang/rust"
	_ "github.com/rohan/jasper/internal/scan/lang/typescript"
)

type Service struct {
	Root  string
	store *store.Store
}

func New(root string) (*Service, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	return &Service{Root: abs, store: store.New(abs)}, nil
}

// FindRoot walks up from dir looking for .jasper, falling back to the git root.
func FindRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for d := abs; ; {
		if st, err := os.Stat(filepath.Join(d, store.Dir)); err == nil && st.IsDir() {
			return d, nil
		}
		if st, err := os.Stat(filepath.Join(d, ".git")); err == nil && st.IsDir() {
			return d, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return abs, nil
		}
		d = parent
	}
}

func (s *Service) Initialised() bool { return s.store.Exists() }

// Check is the load-bearing command: scan, compile, run, report.
func (s *Service) Check() (*engine.Report, error) {
	start := time.Now()

	snap, err := scan.New(s.Root).Scan()
	if err != nil {
		return nil, err
	}
	decisions, err := s.store.Load()
	if err != nil {
		return nil, err
	}
	eng, err := engine.Compile(decisions)
	if err != nil {
		return nil, err
	}
	rep := &engine.Report{
		Findings:  eng.Run(snap),
		Checks:    eng.Len(),
		Decisions: countActive(decisions),
		Files:     len(snap.Files),
		Imports:   len(snap.Imports),
		ElapsedMS: time.Since(start).Milliseconds(),
	}
	return rep, nil
}

func countActive(ds []*model.Decision) int {
	n := 0
	for _, d := range ds {
		if d.Active() {
			n++
		}
	}
	return n
}

func (s *Service) Snapshot() (*model.Snapshot, error) { return scan.New(s.Root).Scan() }

func (s *Service) Decisions() ([]*model.Decision, error) { return s.store.Load() }

// InitPlan is what `jasper init` found and what it offers to write.
type InitPlan struct {
	Snapshot *model.Snapshot
	Facts    []facts.Fact
}

func (s *Service) Plan() (*InitPlan, error) {
	snap, err := s.Snapshot()
	if err != nil {
		return nil, err
	}
	return &InitPlan{Snapshot: snap, Facts: facts.All(snap)}, nil
}

// Proposal is re-exported so a port can build one without importing facts.
// Ports are meant to depend on service alone.
type Proposal = facts.Proposal

// Accept writes a proposal as an accepted decision. Decisions are appended,
// never rewritten, and always land in git.
func (s *Service) Accept(p *facts.Proposal, origin model.Origin) (*model.Decision, error) {
	return s.Record(p, origin, model.StatusAccepted)
}

// Record writes a proposal at an explicit status.
//
// status is what separates a human accepting a decision from an agent
// suggesting one: engine.Compile skips any decision that is not accepted, so a
// StatusProposed decision is inert until a person edits the file. That is the
// whole reason an agent is allowed to write here at all — it can argue for a
// rule change in git, but it cannot approve itself out of a constraint.
func (s *Service) Record(p *facts.Proposal, origin model.Origin, status model.Status) (*model.Decision, error) {
	// Normalise before validating so every caller — facts, MCP, a future port —
	// is checked and written in the one shape the engine reads.
	enforce := NormalizeEnforce(p.Enforce)
	if err := ValidateEnforce(enforce); err != nil {
		return nil, err
	}
	existing, err := s.store.Load()
	if err != nil {
		return nil, err
	}
	d := &model.Decision{
		ID:     store.NextID(existing),
		Title:  p.Title,
		Status: status,
		Origin: origin,
		Date:   time.Now().Format("2006-01-02"),
		Why:    p.Why,
		Brief:  p.Brief,
	}
	path, err := s.store.WriteDecision(d, enforce)
	if err != nil {
		return nil, err
	}
	d.Path = path
	return d, nil
}

func (s *Service) InitStore(name string) error {
	if s.store.Exists() {
		return fmt.Errorf("%s already exists in %s", store.Dir, s.Root)
	}
	return s.store.Init(&store.Config{Version: 1, Name: name})
}

// Brief assembles what a coding agent should know before it starts. It is
// deliberately small: the `brief` field of the decisions that bind, not the
// whole .jasper directory. Context is a packing problem, not a retrieval one.
type Brief struct {
	Project   string          `json:"project"`
	Modules   []model.Module  `json:"modules"`
	Decisions []BriefDecision `json:"decisions"`
	Notes     []string        `json:"notes,omitempty"`
}

type BriefDecision struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Brief string `json:"brief"`
}

func (s *Service) Brief() (*Brief, error) {
	snap, err := s.Snapshot()
	if err != nil {
		return nil, err
	}
	decisions, err := s.store.Load()
	if err != nil {
		return nil, err
	}
	cfg, err := s.store.LoadConfig()
	if err != nil {
		return nil, err
	}
	b := &Brief{Project: cfg.Name, Modules: snap.Modules}
	if b.Project == "" {
		b.Project = filepath.Base(s.Root)
	}
	if len(b.Modules) > 12 {
		b.Modules = b.Modules[:12]
	}
	for _, d := range decisions {
		if !d.Active() || d.Brief == "" {
			continue
		}
		bd := BriefDecision{ID: d.ID, Title: d.Title, Brief: d.Brief}
		if d.Binding() {
			b.Decisions = append(b.Decisions, bd)
		} else {
			b.Notes = append(b.Notes, fmt.Sprintf("%s %s: %s", d.ID, d.Title, d.Brief))
		}
	}
	return b, nil
}
