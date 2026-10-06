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
	f.Directives = []rule.Directive{{Key: "rust_extension", Value: "disabled"}}
	l.Configure(parent, "", f)
	if len(parent.KindMap) != 1 || parent.KindMap["go_library"].KindName != "custom_go" {
		t.Fatalf("disabled mappings = %v", parent.KindMap)
	}
	if parent.AliasMap["outer"] != "rust_library" {
		t.Fatalf("index aliases = %v", parent.AliasMap)
	}
	child := parent.Clone()
	child.KindMap["rust_library"] = config.MappedKind{FromKind: "rust_library", KindName: "child_wrapper", KindLoad: "//:child.bzl"}
	f.Directives = []rule.Directive{{Key: "rust_extension", Value: "enabled"}}
	l.Configure(child, "child", f)
	if child.KindMap["rust_library"].KindName != "child_wrapper" {
		t.Fatalf("local override = %v", child.KindMap)
	}
	sibling := parent.Clone()
	l.Configure(sibling, "sibling", f)
	if sibling.KindMap["rust_library"].KindName != "wrapper" || sibling.KindMap["wrapper"].KindName != "outer" {
		t.Fatalf("inherited mappings = %v", sibling.KindMap)
	}
	if len(parent.KindMap) != 1 {
		t.Fatalf("mutated parent = %v", parent.KindMap)
	}
}
