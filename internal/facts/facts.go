// Package facts derives observations from a snapshot. It is pure and contains
// no inference: `jasper init` must describe the architecture a repository
// actually has, never one a model imagined. An LLM hallucinating your existing
// structure on day one destroys trust permanently, so day one uses no LLM.
package facts

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rohan/jasper/internal/model"
)

// Fact is one observation, optionally with the decision it would justify.
type Fact struct {
	Title    string
	Detail   string
	Holds    bool // true: already true of the code, safe to enforce today
	Proposal *Proposal
}

// Proposal is a decision `init` offers to write. It carries the enforce block
// in raw form so it round-trips into YAML unchanged.
type Proposal struct {
	Title   string
	Why     string
	Brief   string
	Enforce []map[string]any
}

// capability groups packages that do the same job. Two members in one project
// is the signature of agent-generated drift: the agent reached for what it knew
// rather than what the project already had.
var capabilities = map[string][]string{
	// JavaScript / TypeScript
	"HTTP client":      {"axios", "got", "node-fetch", "superagent", "ky", "undici", "request"},
	"date library":     {"moment", "dayjs", "date-fns", "luxon", "js-joda"},
	"state manager":    {"redux", "@reduxjs/toolkit", "zustand", "jotai", "recoil", "mobx", "valtio"},
	"ORM/query layer":  {"prisma", "@prisma/client", "drizzle-orm", "typeorm", "sequelize", "knex", "mikro-orm"},
	"schema validator": {"zod", "yup", "joi", "ajv", "superstruct", "valibot"},
	"test runner":      {"jest", "vitest", "mocha", "ava", "jasmine"},
	"bundler":          {"webpack", "rollup", "parcel", "esbuild", "vite"},

	// Python
	"Python HTTP client":      {"requests", "httpx", "aiohttp", "urllib3", "treq"},
	"Python test runner":      {"pytest", "nose", "nose2", "unittest2"},
	"Python ORM":              {"sqlalchemy", "peewee", "tortoise-orm", "pony", "django"},
	"Python schema validator": {"pydantic", "marshmallow", "cerberus", "voluptuous", "attrs"},
	"Python task queue":       {"celery", "rq", "dramatiq", "huey"},

	// Rust
	"Rust HTTP client":    {"reqwest", "ureq", "isahc", "surf"},
	"Rust async runtime":  {"tokio", "async-std", "smol"},
	"Rust web framework":  {"axum", "actix-web", "rocket", "warp", "tide"},
	"Rust error handling": {"anyhow", "eyre", "failure", "snafu"},

	// Go
	"Go router":  {"github.com/gin-gonic/gin", "github.com/labstack/echo", "github.com/gofiber/fiber", "github.com/go-chi/chi"},
	"Go logger":  {"go.uber.org/zap", "github.com/sirupsen/logrus", "github.com/rs/zerolog"},
	"Go CLI":     {"github.com/spf13/cobra", "github.com/urfave/cli", "github.com/alecthomas/kong"},
	"Go ORM":     {"gorm.io/gorm", "github.com/jmoiron/sqlx", "entgo.io/ent"},
	"Go asserts": {"github.com/stretchr/testify", "github.com/google/go-cmp"},
}

// datastores maps a package to the store it implies.
var datastores = map[string]string{
	// JavaScript / TypeScript
	"pg": "PostgreSQL", "postgres": "PostgreSQL", "@prisma/client": "PostgreSQL (via Prisma)",
	"mysql": "MySQL", "mysql2": "MySQL",
	"mongodb": "MongoDB", "mongoose": "MongoDB",
	"sqlite3": "SQLite", "better-sqlite3": "SQLite",
	"redis": "Redis", "ioredis": "Redis",
	"@aws-sdk/client-dynamodb": "DynamoDB",
	"cassandra-driver":         "Cassandra",

	// Python
	"psycopg": "PostgreSQL", "psycopg2": "PostgreSQL", "psycopg2-binary": "PostgreSQL",
	"asyncpg": "PostgreSQL",
	"pymongo": "MongoDB", "motor": "MongoDB",
	"mysqlclient": "MySQL", "PyMySQL": "MySQL", "aiomysql": "MySQL",
	"redis-py": "Redis", "aioredis": "Redis",
	"boto3": "", // too general to imply a datastore

	// Rust
	"tokio-postgres": "PostgreSQL", "deadpool-postgres": "PostgreSQL",
	"mysql_async": "MySQL",
	"rusqlite":    "SQLite",
	"mongodb-rs":  "MongoDB",

	// Go
	"github.com/lib/pq": "PostgreSQL", "github.com/jackc/pgx/v5": "PostgreSQL",
	"go.mongodb.org/mongo-driver":    "MongoDB",
	"github.com/go-sql-driver/mysql": "MySQL",
	"modernc.org/sqlite":             "SQLite",
	"github.com/redis/go-redis/v9":   "Redis",
}

// All runs every detector.
func All(snap *model.Snapshot) []Fact {
	var out []Fact
	out = append(out, dependencySet(snap))
	if f, ok := datastore(snap); ok {
		out = append(out, f)
	}
	out = append(out, duplicates(snap)...)
	out = append(out, privateDirs(snap)...)
	return out
}

// dependencySet is the flagship proposal: every direct dependency the project
// already declares is approved; anything that arrives later is not.
func dependencySet(snap *model.Snapshot) Fact {
	names := sortedKeys(snap.Manifest.Direct)
	if len(names) == 0 {
		return Fact{Title: "No declared dependencies", Holds: true}
	}
	return Fact{
		Title:  fmt.Sprintf("%d direct dependencies declared", len(names)),
		Detail: preview(names, 6),
		Holds:  true,
		Proposal: &Proposal{
			Title: "Dependencies are chosen deliberately",
			Why: "Coding agents add packages as a side effect of solving a task. " +
				"Every direct dependency should trace back to a moment someone chose it.",
			Brief: "Do not add a direct dependency without recording a decision. " +
				"Prefer what the project already uses.",
			Enforce: []map[string]any{{
				"approved_dependencies": map[string]any{
					"allow":   names,
					"message": "dependency added without a decision",
				},
			}},
		},
	}
}

func datastore(snap *model.Snapshot) (Fact, bool) {
	found := map[string][]string{}
	for name := range snap.Manifest.Direct {
		// An empty mapping means "recognised, but implies no specific store".
		if store, ok := datastores[name]; ok && store != "" {
			found[store] = append(found[store], name)
		}
	}
	if len(found) == 0 {
		return Fact{}, false
	}
	stores := sortedKeys(found)
	var forbidden []string
	for pkg, store := range datastores {
		if store == "" {
			continue
		}
		if _, inUse := found[store]; !inUse {
			forbidden = append(forbidden, pkg)
		}
	}
	sort.Strings(forbidden)

	return Fact{
		Title:  fmt.Sprintf("Datastore: %s", strings.Join(stores, " + ")),
		Detail: "no other database driver is declared",
		Holds:  true,
		Proposal: &Proposal{
			Title: fmt.Sprintf("Datastore is %s", strings.Join(stores, " + ")),
			Why: "A second datastore arriving quietly is one of the most expensive kinds of drift: " +
				"it splits transactions, backups and operational knowledge before anyone reviews the choice.",
			Brief: fmt.Sprintf("Persistence is %s. Adding another datastore requires superseding this decision.",
				strings.Join(stores, " + ")),
			Enforce: []map[string]any{{
				"forbid_dependency": map[string]any{
					"packages": forbidden,
					"message":  fmt.Sprintf("this project stores data in %s", strings.Join(stores, " + ")),
				},
			}},
		},
	}, true
}

func duplicates(snap *model.Snapshot) []Fact {
	var out []Fact
	for _, cap := range sortedKeys(capabilities) {
		var present []string
		for _, pkg := range capabilities[cap] {
			if _, ok := snap.Manifest.Direct[pkg]; ok {
				present = append(present, pkg)
			}
		}
		if len(present) < 2 {
			continue
		}
		sort.Strings(present)
		out = append(out, Fact{
			Title:  fmt.Sprintf("Two %ss: %s", cap, strings.Join(present, " and ")),
			Detail: "one of these is probably drift — pick one before enforcing",
			Holds:  false,
		})
	}
	return out
}

// privateDirs finds directories named in a way that signals privacy and that
// nothing outside their parent actually imports. Those are boundaries the code
// already respects, so they can be enforced today at no cost.
func privateDirs(snap *model.Snapshot) []Fact {
	const marker = "/internal/"
	owners := map[string]string{} // "src/identity/internal" -> "src/identity"
	for id := range snap.Files {
		p := string(id)
		i := strings.Index(p, marker)
		if i < 0 {
			continue
		}
		dir := p[:i+len(marker)-1]
		owners[dir] = p[:i]
	}
	var out []Fact
	for _, dir := range sortedKeys(owners) {
		owner := owners[dir]
		violations := 0
		for _, im := range snap.Imports {
			if im.External() || !strings.HasPrefix(string(im.To), dir+"/") {
				continue
			}
			if !strings.HasPrefix(string(im.From), owner+"/") {
				violations++
			}
		}
		f := Fact{
			Title: fmt.Sprintf("%s is private to %s", dir, owner),
			Holds: violations == 0,
		}
		if violations > 0 {
			f.Detail = fmt.Sprintf("%d imports already reach in from outside", violations)
			out = append(out, f)
			continue
		}
		f.Detail = "nothing outside imports it today"
		f.Proposal = &Proposal{
			Title: fmt.Sprintf("%s internals are private", owner),
			Why: fmt.Sprintf("Everything under %s is an implementation detail of %s. "+
				"Code that reaches past the public entry point turns a local change into a breaking one.",
				dir, owner),
			Brief: fmt.Sprintf("Import %s only through its public entry point. Never from %s.", owner, dir),
			Enforce: []map[string]any{{
				"no_import": map[string]any{
					"from":    "**",
					"to":      dir + "/**",
					"except":  owner + "/**",
					"message": fmt.Sprintf("%s is private to %s", dir, owner),
				},
			}},
		}
		out = append(out, f)
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func preview(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(items[:n], ", "), len(items)-n)
}
