package checks

import (
	"testing"

	"github.com/rohan/jasper/internal/engine"
	"github.com/rohan/jasper/internal/model"
)

// runCheck compiles a check and runs it, failing the test on a compile error.
func runCheck(t *testing.T, c engine.Check, cfg model.RawConfig, snap *model.Snapshot) []model.Finding {
	t.Helper()
	r, err := c.Compile(cfg, &model.Decision{ID: "DEC-001", Path: "test.yaml"})
	if err != nil {
		t.Fatalf("compile %s: %v", c.Kind(), err)
	}
	return r.Run(snap)
}

// mustReject asserts a config is refused at compile time. A rule that compiles
// but enforces nothing is the failure mode every check here guards against.
func mustReject(t *testing.T, c engine.Check, cfg model.RawConfig) {
	t.Helper()
	if _, err := c.Compile(cfg, &model.Decision{ID: "DEC-001"}); err == nil {
		t.Fatalf("%s accepted a config it should have rejected: %v", c.Kind(), cfg)
	}
}

// snapOf builds a snapshot with files and imports.
func snapOf(files []model.FileID, imports ...model.Import) *model.Snapshot {
	fs := make(map[model.FileID]*model.File, len(files))
	for _, id := range files {
		fs[id] = &model.File{ID: id}
	}
	return &model.Snapshot{Files: fs, Imports: imports}
}

// snapText builds a snapshot whose files have contents.
func snapText(bodies map[model.FileID]string) *model.Snapshot {
	fs := make(map[model.FileID]*model.File, len(bodies))
	for id := range bodies {
		fs[id] = &model.File{ID: id}
	}
	snap := &model.Snapshot{Files: fs}
	snap.SetLoader(func(id model.FileID) []byte { return []byte(bodies[id]) })
	return snap
}

func snapManifest(direct map[string]string, files ...model.FileID) *model.Snapshot {
	return &model.Snapshot{
		Files:    map[model.FileID]*model.File{},
		Manifest: model.Manifest{Manager: "npm", Direct: direct, Files: files},
	}
}

// jasperStack is the shape `layers` is designed for: a real stack.
var jasperStack = model.RawConfig{"order": []any{
	[]any{"internal/model/**", "internal/glob/**"},
	[]any{"internal/scan/**", "internal/engine/**"},
	"internal/service/**",
}}
