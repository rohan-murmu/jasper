package model

// Origin records where a decision came from. Observed decisions were extracted
// from code that already behaves that way; Authored ones come from a design
// session (v3). The field costs nothing now and means design-mode decisions are
// first-class the day that feature lands.
type Origin string

const (
	OriginObserved Origin = "observed"
	OriginProposed Origin = "proposed"
	OriginAuthored Origin = "authored"
)

type Status string

const (
	StatusAccepted   Status = "accepted"
	StatusProposed   Status = "proposed"
	StatusSuperseded Status = "superseded"
)

// RawConfig is an undecoded enforce-rule body. model stays dependency-free, so
// the YAML is decoded into plain maps here and typed by each check at compile
// time.
type RawConfig map[string]any

type EnforceRule struct {
	Kind string
	Cfg  RawConfig
}

// Decision is the central object. Three audiences, one file:
//
//	Why   - the human reading it in six months
//	Brief - the coding agent, injected before it works
//	Enforce - the machine, compiled into checks that fail the build
//
// A decision with no Enforce block is a note, not a rule, and is rendered as
// such.
type Decision struct {
	ID           string        `yaml:"id"`
	Title        string        `yaml:"title"`
	Status       Status        `yaml:"status"`
	Origin       Origin        `yaml:"origin"`
	Date         string        `yaml:"date"`
	Why          string        `yaml:"why"`
	Brief        string        `yaml:"brief"`
	Enforce      []EnforceRule `yaml:"-"`
	Affects      []string      `yaml:"affects,omitempty"`
	Supersedes   string        `yaml:"supersedes,omitempty"`
	SupersededBy string        `yaml:"superseded_by,omitempty"`
	Scope        *Scope        `yaml:"scope,omitempty"`
	Path         string        `yaml:"-"` // source file, for error messages
}

// Scope grandfathers existing violations. Without this, the first run on a real
// repo emits hundreds of failures and the user disables Jasper permanently.
type Scope struct {
	Since   string   `yaml:"since"`   // git rev: only files touched after this
	Exclude []string `yaml:"exclude"` // known exceptions, each ideally with an owner
}

func (d *Decision) Active() bool {
	return d.Status == StatusAccepted && d.SupersededBy == ""
}

// Binding reports whether this decision actually constrains anything.
func (d *Decision) Binding() bool { return len(d.Enforce) > 0 }
