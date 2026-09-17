package checks

import (
	"sync"
	"testing"

	"github.com/rohan/jasper/internal/engine"
	"github.com/rohan/jasper/internal/model"
)

var _ engine.Check = ForbidText{}

func TestForbidTextReportsEveryMatchWithLine(t *testing.T) {
	snap := snapText(map[model.FileID]string{
		"src/a.js": "const a = 1\nconsole.log(a)\nconst b = 2\nconsole.log(b)\n",
	})
	got := runCheck(t, ForbidText{}, model.RawConfig{
		"pattern": `console\.log`, "in": "src/**",
	}, snap)
	if len(got) != 2 {
		t.Fatalf("got %d findings, want 2", len(got))
	}
	if got[0].Line != 2 || got[1].Line != 4 {
		t.Errorf("lines = %d,%d, want 2,4", got[0].Line, got[1].Line)
	}
	if got[0].Evidence == "" {
		t.Error("a text finding must quote the offending line")
	}
}

func TestForbidTextHonoursInAndExcept(t *testing.T) {
	snap := snapText(map[model.FileID]string{
		"src/app.js":         "process.env.PORT\n",
		"src/configs/env.js": "process.env.PORT\n",
		"scripts/build.js":   "process.env.PORT\n",
	})
	got := runCheck(t, ForbidText{}, model.RawConfig{
		"pattern": `process\.env`,
		"in":      "src/**",
		"except":  []any{"src/configs/**"},
	}, snap)
	if len(got) != 1 {
		t.Fatalf("only src/app.js should match; got %d: %v", len(got), got)
	}
	if got[0].File != "src/app.js" {
		t.Errorf("file = %s, want src/app.js", got[0].File)
	}
}

func TestForbidTextRejectsBadPattern(t *testing.T) {
	mustReject(t, ForbidText{}, model.RawConfig{"pattern": "([unclosed"})
	mustReject(t, ForbidText{}, model.RawConfig{})
}

func TestForbidTextFindingsAreDeterministic(t *testing.T) {
	bodies := map[model.FileID]string{}
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		bodies[model.FileID("src/"+n+".js")] = "TODO\n"
	}
	cfg := model.RawConfig{"pattern": "TODO"}
	first := runCheck(t, ForbidText{}, cfg, snapText(bodies))
	for i := 0; i < 5; i++ {
		again := runCheck(t, ForbidText{}, cfg, snapText(bodies))
		for j := range first {
			if first[j].File != again[j].File {
				t.Fatalf("run %d differs at %d: %s vs %s — map order leaked into output",
					i, j, first[j].File, again[j].File)
			}
		}
	}
}

// Engine.Run executes checks concurrently and Snapshot.Text caches what it
// loads. Before the cache was guarded, two text checks on one snapshot were a
// concurrent map write. Run this with -race.
func TestForbidTextConcurrentRunsAreSafe(t *testing.T) {
	bodies := map[model.FileID]string{}
	for i := 0; i < 40; i++ {
		bodies[model.FileID("src/f"+string(rune('a'+i%26))+".js")] = "secret\nTODO\n"
	}
	snap := snapText(bodies)

	r1, err := ForbidText{}.Compile(model.RawConfig{"pattern": "secret"}, &model.Decision{ID: "DEC-001"})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := ForbidText{}.Compile(model.RawConfig{"pattern": "TODO"}, &model.Decision{ID: "DEC-002"})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); r1.Run(snap) }()
		go func() { defer wg.Done(); r2.Run(snap) }()
	}
	wg.Wait()
}
