package mcp

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/rohan/jasper/internal/service"
)

const fixture = "../../../testdata/acme-api"

// drive runs a session and returns one decoded response per reply written.
func drive(t *testing.T, root string, frames ...string) []map[string]any {
	t.Helper()
	svc, err := service.New(root)
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	var out strings.Builder
	srv := NewServer(svc, io.Discard)
	if err := srv.Serve(strings.NewReader(strings.Join(frames, "\n")), &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var got []map[string]any
	dec := json.NewDecoder(strings.NewReader(out.String()))
	for {
		var m map[string]any
		if err := dec.Decode(&m); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("decode reply: %v (raw: %q)", err, out.String())
		}
		got = append(got, m)
	}
	return got
}

// callText returns the text payload of a tools/call result.
func callText(t *testing.T, m map[string]any) string {
	t.Helper()
	res, ok := m["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in %v", m)
	}
	content, ok := res["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("no content in %v", res)
	}
	first, _ := content[0].(map[string]any)
	s, _ := first["text"].(string)
	return s
}

func TestInitializeAdvertisesTools(t *testing.T) {
	got := drive(t, fixture,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	if len(got) != 1 {
		t.Fatalf("got %d replies, want 1", len(got))
	}
	res := got[0]["result"].(map[string]any)
	if res["protocolVersion"] != "2025-06-18" {
		t.Errorf("protocolVersion = %v, want the version the client asked for", res["protocolVersion"])
	}
	caps := res["capabilities"].(map[string]any)
	if _, ok := caps["tools"]; !ok {
		t.Error("capabilities must advertise tools, or no client will call tools/list")
	}
	if s, _ := res["instructions"].(string); !strings.Contains(s, "can_import") {
		t.Error("instructions should tell the agent when to pre-flight an import")
	}
}

// An unknown protocol revision must still get a usable answer rather than an
// error, so a newer client can negotiate down.
func TestInitializeFallsBackOnUnknownProtocol(t *testing.T) {
	got := drive(t, fixture,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	res := got[0]["result"].(map[string]any)
	if res["protocolVersion"] != defaultProtocol {
		t.Errorf("protocolVersion = %v, want fallback %s", res["protocolVersion"], defaultProtocol)
	}
}

// Replying to a notification is a protocol violation that desynchronises
// strict clients, so this is worth pinning.
func TestNotificationsGetNoReply(t *testing.T) {
	got := drive(t, fixture,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	if len(got) != 1 {
		t.Fatalf("got %d replies, want 1 — a notification must not be answered", len(got))
	}
	if got[0]["id"] != float64(2) {
		t.Errorf("the one reply should be for the ping, got id %v", got[0]["id"])
	}
}

func TestToolsListIsComplete(t *testing.T) {
	got := drive(t, fixture, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tools := got[0]["result"].(map[string]any)["tools"].([]any)

	want := map[string]bool{
		"architecture_brief": false, "can_import": false, "can_add_dependency": false,
		"check_architecture": false, "explain_decision": false, "list_decisions": false,
		"propose_decision": false,
	}
	for _, raw := range tools {
		tool := raw.(map[string]any)
		name, _ := tool["name"].(string)
		if _, expected := want[name]; !expected {
			t.Errorf("unexpected tool %q", name)
			continue
		}
		want[name] = true
		if d, _ := tool["description"].(string); len(d) < 40 {
			t.Errorf("%s: description is the only thing telling an agent when to call it", name)
		}
		if _, ok := tool["inputSchema"].(map[string]any); !ok {
			t.Errorf("%s: missing inputSchema", name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("tool %q missing from tools/list", name)
		}
	}
}

func TestUnknownMethodIsMethodNotFound(t *testing.T) {
	got := drive(t, fixture, `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`)
	err, ok := got[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("want an error for an unsupported method, got %v", got[0])
	}
	if err["code"] != float64(codeMethodNotFound) {
		t.Errorf("code = %v, want %d", err["code"], codeMethodNotFound)
	}
}

// A malformed frame must not kill the session: the client would lose every
// later call with no way to tell why.
func TestParseErrorDoesNotEndSession(t *testing.T) {
	got := drive(t, fixture,
		`{"jsonrpc":"2.0","id":1,`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	if len(got) < 2 {
		t.Fatalf("got %d replies, want a parse error plus the ping answer", len(got))
	}
	if e, ok := got[0]["error"].(map[string]any); !ok || e["code"] != float64(codeParse) {
		t.Errorf("first reply should be a parse error, got %v", got[0])
	}
}

func TestCanImportDeniesBoundaryViolation(t *testing.T) {
	got := drive(t, fixture, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
		`{"name":"can_import","arguments":{"from":"src/db/pool.ts","spec":"@/identity/internal/session"}}}`)
	text := callText(t, got[0])
	if !strings.HasPrefix(text, "DENIED") {
		t.Fatalf("want DENIED, got:\n%s", text)
	}
	for _, want := range []string{"DEC-001", "explain_decision"} {
		if !strings.Contains(text, want) {
			t.Errorf("a denial must name %q so the agent can act on it:\n%s", want, text)
		}
	}
}

// `except` exempts the owning module, and the pre-flight must honour it or the
// agent is blocked from writing correct code.
func TestCanImportAllowsExemptImporter(t *testing.T) {
	got := drive(t, fixture, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
		`{"name":"can_import","arguments":{"from":"src/identity/helper.ts","spec":"./internal/session"}}}`)
	if text := callText(t, got[0]); !strings.HasPrefix(text, "ALLOWED") {
		t.Fatalf("identity may use its own internals; got:\n%s", text)
	}
}

// A repo with pre-existing violations must not answer DENIED to everything.
func TestPreExistingViolationsDoNotDenyUnrelatedChanges(t *testing.T) {
	got := drive(t, fixture, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
		`{"name":"can_add_dependency","arguments":{"package":"lodash"}}}`)
	text := callText(t, got[0])
	if !strings.HasPrefix(text, "ALLOWED") {
		t.Fatalf("unrelated dependency should be allowed; got:\n%s", text)
	}
	if !strings.Contains(text, "already exist") {
		t.Error("the verdict should still disclose the repo's existing violations")
	}
}

// Asking about something the code already does must not read as approval.
func TestAlreadyPresentViolationIsReportedAsLive(t *testing.T) {
	got := drive(t, fixture, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
		`{"name":"can_add_dependency","arguments":{"package":"mongoose"}}}`)
	text := callText(t, got[0])
	if !strings.HasPrefix(text, "DENIED") {
		t.Fatalf("mongoose is declared and forbidden; want DENIED, got:\n%s", text)
	}
	if !strings.Contains(text, "ALREADY") {
		t.Error("the agent should be told this is a live failure, not a hypothetical")
	}
}

// Ruby is deliberately used here: it must be a language Jasper does not
// handle. This test previously used main.rs and started passing vacuously the
// day the Rust adapter landed — if Ruby support is ever added, pick another.
func TestUnsupportedLanguageIsAToolError(t *testing.T) {
	got := drive(t, fixture, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
		`{"name":"can_import","arguments":{"from":"app/main.rb","spec":"sinatra"}}}`)
	res := got[0]["result"].(map[string]any)
	if res["isError"] != true {
		t.Errorf("a file type jasper cannot parse must be an error, not a silent ALLOWED: %v", res)
	}
}

func TestMissingRequiredArgIsInvalidParams(t *testing.T) {
	got := drive(t, fixture, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
		`{"name":"can_import","arguments":{"from":"src/a.ts"}}}`)
	err, ok := got[0]["error"].(map[string]any)
	if !ok || err["code"] != float64(codeInvalidParams) {
		t.Errorf("want invalid params, got %v", got[0])
	}
}
