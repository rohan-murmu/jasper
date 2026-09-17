// Package cli is a port: it parses arguments, calls exactly one service method
// per command, and renders. No command may contain business logic — that keeps
// adding the MCP server (and later an LSP) cheap.
package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/rohan/jasper/internal/engine"
	"github.com/rohan/jasper/internal/facts"
	"github.com/rohan/jasper/internal/model"
	"github.com/rohan/jasper/internal/ports/mcp"
	"github.com/rohan/jasper/internal/service"
)

const Version = "0.1.0"

const usage = `jasper — architecture decisions your coding agent has to honor

usage: jasper <command> [flags]

  init              observe this repository and propose decisions
  check             verify the code against every accepted decision
  ls                list decisions
  why <DEC-id>      explain a decision: rationale, rules, origin
  brief             what a coding agent should know before it starts
  mcp               run as an MCP server on stdio (for Claude Code, Cursor, ...)
  checks            list available check kinds
  version

flags:
  -C <dir>          run as if started in <dir>
  --format <fmt>    check output: text (default), json, github
  --yes             init: accept every proposal without asking
`

func Main(args []string) int {
	dir := "."
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-C" {
			dir = args[i+1]
			args = append(args[:i:i], args[i+2:]...)
			break
		}
	}
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "version", "--version", "-v":
		fmt.Println("jasper " + Version)
		return 0
	case "help", "--help", "-h":
		fmt.Print(usage)
		return 0
	case "checks":
		for _, k := range engine.Kinds() {
			fmt.Println(k)
		}
		return 0
	}

	root, err := service.FindRoot(dir)
	if err != nil {
		return fail(err)
	}
	svc, err := service.New(root)
	if err != nil {
		return fail(err)
	}

	switch cmd {
	case "init":
		return cmdInit(svc, rest)
	case "check":
		return cmdCheck(svc, rest)
	case "ls":
		return cmdLs(svc)
	case "why":
		return cmdWhy(svc, rest)
	case "brief":
		return cmdBrief(svc, rest)
	case "mcp":
		return cmdMCP(svc)
	default:
		fmt.Fprintf(os.Stderr, "jasper: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
}

func cmdCheck(svc *service.Service, args []string) int {
	format := flagValue(args, "--format", "text")
	if !svc.Initialised() {
		fmt.Fprintln(os.Stderr, "jasper: no .jasper directory here — run `jasper init` first")
		return 2
	}
	rep, err := svc.Check()
	if err != nil {
		return fail(err)
	}
	titles, paths, err := decisionIndex(svc)
	if err != nil {
		return fail(err)
	}

	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	case "github":
		renderGitHub(os.Stdout, rep, titles)
	default:
		renderText(os.Stdout, rep, titles, paths)
	}
	if rep.OK() {
		return 0
	}
	return 1
}

func cmdInit(svc *service.Service, args []string) int {
	auto := has(args, "--yes")
	if !svc.Initialised() {
		if err := svc.InitStore(""); err != nil {
			return fail(err)
		}
	}
	plan, err := svc.Plan()
	if err != nil {
		return fail(err)
	}
	s := newStyle(os.Stdout)
	snap := plan.Snapshot

	fmt.Printf("%sScanning%s %s\n\n", s.bold, s.reset, svc.Root)
	fmt.Printf("  files        %d\n  imports      %d\n  dependencies %d (%s)\n",
		len(snap.Files), len(snap.Imports), len(snap.Manifest.Direct), orDash(snap.Manifest.Manager))
	if len(snap.Modules) > 0 {
		fmt.Printf("\n  %sModules%s\n", s.bold, s.reset)
		for i, m := range snap.Modules {
			if i == 8 {
				fmt.Printf("    %s… %d more%s\n", s.dim, len(snap.Modules)-8, s.reset)
				break
			}
			fmt.Printf("    %-28s %4d files\n", m.Glob, m.Files)
		}
	}

	fmt.Printf("\n  %sWhat is true of your code right now%s\n", s.bold, s.reset)
	var proposals []*facts.Proposal
	for _, f := range plan.Facts {
		mark, col := "✓", s.green
		if !f.Holds {
			mark, col = "✗", s.yellow
		}
		fmt.Printf("    %s%s%s %s", col, mark, s.reset, f.Title)
		if f.Detail != "" {
			fmt.Printf("  %s%s%s", s.dim, f.Detail, s.reset)
		}
		fmt.Println()
		if f.Proposal != nil && f.Holds {
			proposals = append(proposals, f.Proposal)
		}
	}

	if len(proposals) == 0 {
		fmt.Printf("\n%sNothing safe to enforce yet.%s Fix what is marked ✗, then run init again.\n", s.dim, s.reset)
		return 0
	}

	fmt.Printf("\n  %s%d decisions can be enforced today%s %s(each already passes on HEAD)%s\n",
		s.bold, len(proposals), s.reset, s.dim, s.reset)

	in := bufio.NewReader(os.Stdin)
	written := 0
	for _, p := range proposals {
		fmt.Printf("\n  %s◆%s %s\n", s.cyan, s.reset, p.Title)
		for _, e := range p.Enforce {
			for kind, cfg := range e {
				fmt.Printf("    enforce  %s%s%s %s%v%s\n", s.bold, kind, s.reset, s.dim, compact(cfg), s.reset)
			}
		}
		if !auto {
			fmt.Printf("    [a]ccept  [s]kip > ")
			line, _ := in.ReadString('\n')
			if a := strings.TrimSpace(strings.ToLower(line)); a != "a" && a != "" {
				continue
			}
		}
		d, err := svc.Accept(p, model.OriginObserved)
		if err != nil {
			return fail(err)
		}
		written++
		fmt.Printf("    %s✓ %s written%s\n", s.green, d.ID, s.reset)
	}

	fmt.Printf("\n%s%d decisions in .jasper/decisions%s — run %sjasper check%s\n",
		s.bold, written, s.reset, s.cyan, s.reset)
	return 0
}

func cmdLs(svc *service.Service) int {
	ds, err := svc.Decisions()
	if err != nil {
		return fail(err)
	}
	s := newStyle(os.Stdout)
	if len(ds) == 0 {
		fmt.Println("no decisions yet — run `jasper init`")
		return 0
	}
	for _, d := range ds {
		kind, col := "note ", s.dim
		if d.Binding() {
			kind, col = "rule ", s.green
		}
		if !d.Active() {
			kind, col = "super", s.dim
		}
		fmt.Printf("%s%s%s  %s%-9s%s %s%s\n",
			col, kind, s.reset, s.bold, d.ID, s.reset, d.Title,
			dimRules(s, len(d.Enforce)))
	}
	return 0
}

func dimRules(s style, n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%s  (%d)%s", s.dim, n, s.reset)
}

func cmdWhy(svc *service.Service, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: jasper why <DEC-id>")
		return 2
	}
	ds, err := svc.Decisions()
	if err != nil {
		return fail(err)
	}
	want := strings.ToUpper(args[0])
	s := newStyle(os.Stdout)
	for _, d := range ds {
		if !strings.EqualFold(d.ID, want) && !strings.Contains(strings.ToUpper(d.ID), want) {
			continue
		}
		fmt.Printf("%s%s  %s%s\n", s.bold, d.ID, d.Title, s.reset)
		fmt.Printf("%s%s · %s · %s%s\n\n", s.dim, d.Status, orDash(string(d.Origin)), orDash(d.Date), s.reset)
		if d.Why != "" {
			fmt.Printf("%s\n", strings.TrimSpace(d.Why))
		}
		if d.Brief != "" {
			fmt.Printf("\n%sagent brief%s\n%s\n", s.bold, s.reset, strings.TrimSpace(d.Brief))
		}
		if len(d.Enforce) > 0 {
			fmt.Printf("\n%senforced by%s\n", s.bold, s.reset)
			for _, e := range d.Enforce {
				fmt.Printf("  %s %s%v%s\n", e.Kind, s.dim, compact(e.Cfg), s.reset)
			}
		} else {
			fmt.Printf("\n%snot enforced — this is a note, not a rule%s\n", s.dim, s.reset)
		}
		fmt.Printf("\n%s%s%s\n", s.dim, d.Path, s.reset)
		return 0
	}
	fmt.Fprintf(os.Stderr, "jasper: no decision matching %q\n", want)
	return 1
}

func cmdBrief(svc *service.Service, args []string) int {
	b, err := svc.Brief()
	if err != nil {
		return fail(err)
	}
	if flagValue(args, "--format", "text") == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(b)
		return 0
	}
	fmt.Printf("# Architecture decisions for %s\n\n", b.Project)
	fmt.Printf("These are binding. `jasper check` fails the build when code contradicts them.\n\n")
	for _, d := range b.Decisions {
		fmt.Printf("- **%s %s** — %s\n", d.ID, d.Title, strings.TrimSpace(d.Brief))
	}
	if len(b.Modules) > 0 {
		fmt.Printf("\n## Modules\n\n")
		for _, m := range b.Modules {
			fmt.Printf("- `%s` (%d files)\n", m.Glob, m.Files)
		}
	}
	if len(b.Notes) > 0 {
		fmt.Printf("\n## Context (not enforced)\n\n")
		for _, n := range b.Notes {
			fmt.Printf("- %s\n", n)
		}
	}
	return 0
}

// cmdMCP hands stdio to the MCP port. It stays available in an uninitialised
// repo so an agent can connect first and run init afterwards; the tools simply
// report that nothing is enforced yet.
func cmdMCP(svc *service.Service) int {
	if !svc.Initialised() {
		fmt.Fprintf(os.Stderr, "jasper mcp: no %s in %s — tools will report that nothing is enforced\n",
			".jasper", svc.Root)
	}
	if err := mcp.NewServer(svc, os.Stderr).Serve(os.Stdin, os.Stdout); err != nil {
		return fail(err)
	}
	return 0
}

func decisionIndex(svc *service.Service) (titles, paths map[string]string, err error) {
	ds, err := svc.Decisions()
	if err != nil {
		return nil, nil, err
	}
	titles, paths = map[string]string{}, map[string]string{}
	for _, d := range ds {
		titles[d.ID] = d.Title
		paths[d.ID] = d.Path
	}
	return titles, paths, nil
}

func compact(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	s := string(b)
	if len(s) > 96 {
		s = s[:93] + "..."
	}
	return s
}

func flagValue(args []string, name, def string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			return v
		}
	}
	return def
}

func has(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func fail(err error) int {
	fmt.Fprintf(os.Stderr, "jasper: %v\n", err)
	return 2
}
