package typescript

import (
	"testing"

	"github.com/rohan/jasper/internal/scan"
)

func TestImports(t *testing.T) {
	src := []byte(`
// import { fake } from 'commented-out'
/* import { alsoFake } from 'block-comment' */
import React from 'react'
import { a, b } from "./local"
import type { Cfg } from '../types'
import * as z from 'zod'
import 'side-effect.css'
export { x } from './reexport'
export * from './star'
const fs = require('node:fs')
const lazy = await import('./dynamic')
const notAnImport = "import x from 'string-literal'"
const re = /import .* from 'regex'/
const tpl = ` + "`import from ${x} 'template'`" + `
`)
	got, err := Lang{}.Imports(src)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"react", "./local", "../types", "zod", "side-effect.css",
		"./reexport", "./star", "node:fs", "./dynamic",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d imports %v, want %d %v", len(got), specs(got), len(want), want)
	}
	for i := range want {
		if got[i].Spec != want[i] {
			t.Errorf("import %d = %q, want %q", i, got[i].Spec, want[i])
		}
	}
	for _, im := range got {
		if im.Spec == "../types" && !im.Type {
			t.Error("../types should be marked type-only")
		}
		if im.Spec == "react" && im.Type {
			t.Error("react should not be type-only")
		}
	}
}

func specs(in []scan.RawImport) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = v.Spec
	}
	return out
}
