package rs

import (
	"testing"

	"github.com/bazelbuild/bazel-gazelle/config"
	"github.com/bazelbuild/bazel-gazelle/rule"
)

func TestMappingScopeDoesNotMutateParentOrOtherLanguages(t *testing.T) {
	parent := config.New()
	parent.RepoRoot = t.TempDir()
	parent.KindMap = map[string]config.MappedKind{
		"rust_library": {FromKind: "rust_library", KindName: "wrapper", KindLoad: "//:rules.bzl"},
		"wrapper":      {FromKind: "wrapper", KindName: "outer", KindLoad: "//:rules.bzl"},
		"go_library":   {FromKind: "go_library", KindName: "custom_go", KindLoad: "//:go.bzl"},
	}
	l := NewLanguage()
	f := rule.EmptyFile("BUILD.bazel", "")
	parent.Langs = []string{"go"}
	l.Configure(parent, "", f)
	if len(parent.KindMap) != 1 || parent.KindMap["go_library"].KindName != "custom_go" {
		t.Fatalf("disabled mappings = %v", parent.KindMap)
	}
	if parent.AliasMap["outer"] != "rust_library" {
		t.Fatalf("index aliases = %v", parent.AliasMap)
	}
	child := parent.Clone()
	child.KindMap["rust_library"] = config.MappedKind{FromKind: "rust_library", KindName: "child_wrapper", KindLoad: "//:child.bzl"}
	child.Langs = []string{"go", "rs"}
	l.Configure(child, "child", f)
	if child.KindMap["rust_library"].KindName != "child_wrapper" {
		t.Fatalf("local override = %v", child.KindMap)
	}
	sibling := parent.Clone()
	sibling.Langs = nil
	l.Configure(sibling, "sibling", f)
	if sibling.KindMap["rust_library"].KindName != "wrapper" || sibling.KindMap["wrapper"].KindName != "outer" {
		t.Fatalf("inherited mappings = %v", sibling.KindMap)
	}
	if len(parent.KindMap) != 1 {
		t.Fatalf("mutated parent = %v", parent.KindMap)
	}
}

func TestMappingSelection(t *testing.T) {
	for _, tc := range []struct {
		name           string
		langs          []string
		legacyDisabled bool
		wantMapped     bool
	}{
		{name: "all_languages", wantMapped: true},
		{name: "rust_selected", langs: []string{"go", "rs"}, wantMapped: true},
		{name: "other_languages", langs: []string{"go", "proto"}},
		{name: "legacy_disable", langs: []string{"rs"}, legacyDisabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := config.New()
			c.RepoRoot = t.TempDir()
			c.Langs = tc.langs
			c.KindMap = map[string]config.MappedKind{
				"rust_library": {FromKind: "rust_library", KindName: "wrapper", KindLoad: "//:rules.bzl"},
			}
			f := rule.EmptyFile("BUILD.bazel", "")
			if tc.legacyDisabled {
				f.Directives = []rule.Directive{{Key: "rust_extension", Value: "disabled"}}
			}
			NewLanguage().Configure(c, "", f)
			_, mapped := c.KindMap["rust_library"]
			if mapped != tc.wantMapped {
				t.Fatalf("mapping enabled = %v, want %v", mapped, tc.wantMapped)
			}
		})
	}
}
