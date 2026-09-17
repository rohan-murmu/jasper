package model

import "fmt"

type Severity string

const (
	SevError Severity = "error"
	SevWarn  Severity = "warn"
)

// Finding is one violation. Hint is mandatory in spirit: an agent that receives
// a Finding must be able to act on it without asking a human what to do.
type Finding struct {
	Decision string   `json:"decision"`
	Rule     string   `json:"rule"`
	File     FileID   `json:"file"`
	Line     int      `json:"line,omitempty"`
	Message  string   `json:"message"`
	Hint     string   `json:"hint,omitempty"`
	Evidence string   `json:"evidence,omitempty"`
	Severity Severity `json:"severity"`
}

func (f Finding) Location() string {
	if f.Line > 0 {
		return fmt.Sprintf("%s:%d", f.File, f.Line)
	}
	return string(f.File)
}
