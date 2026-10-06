package rs

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/bazelbuild/bazel-gazelle/config"
	"github.com/bazelbuild/bazel-gazelle/label"
	"github.com/bazelbuild/bazel-gazelle/resolve"
	"github.com/bazelbuild/bazel-gazelle/rule"
)

func TestCargoResolution(t *testing.T) {
	regular := externalCrate{label: label.New("crates", "", "serde-1.0.0"), name: "serde"}
	newer := externalCrate{label: label.New("crates", "", "serde-2.0.0"), name: "serde"}
	macro := externalCrate{label: label.New("crates", "", "derive-1.0.0"), name: "derive", macro: true}
	index := &cargoIndex{
		crates:   map[string][]externalCrate{"serde": {regular, newer}, "derive": {macro}},
		packages: map[string]map[string][]externalCrate{"scoped": {"serde": {regular}, "renamed": {regular}}},
		byLabel:  map[string]externalCrate{regular.label.String(): regular, macro.label.String(): macro},
	}
	for _, tc := range []struct {
		name, pkg, imported, override, internal, aliasExpr string
		wantDeps, wantMacros                               []string
		wantAlias                                          bool
	}{
		{name: "ambiguous_preserves_all", imported: "serde", wantDeps: []string{"//old:dep"}, wantMacros: []string{"//old:macro"}},
		{name: "selected_version", pkg: "scoped", imported: "serde", wantDeps: []string{"@crates//:serde-1.0.0"}},
		{name: "proc_macro", imported: "derive", wantMacros: []string{"@crates//:derive-1.0.0"}},
		{name: "override_classifies_macro", imported: "derive", override: "@crates//:derive-1.0.0", wantMacros: []string{"@crates//:derive-1.0.0"}},
		{name: "override_wins", imported: "serde", override: "//chosen:serde", wantDeps: []string{"//chosen:serde"}},
		{name: "internal_wins", imported: "serde", internal: "serde", wantDeps: []string{"//internal:serde"}},
		{name: "alias", pkg: "scoped", imported: "renamed", wantDeps: []string{"@crates//:serde-1.0.0"}, wantAlias: true},
		{name: "conflicting_alias_preserved", pkg: "scoped", imported: "serde", aliasExpr: `{"@crates//:serde-1.0.0": "wrong"}`, wantDeps: []string{"//old:dep"}, wantMacros: []string{"//old:macro"}},
		{name: "computed_aliases_preserved", pkg: "scoped", imported: "renamed", aliasExpr: "ALIASES", wantDeps: []string{"//old:dep"}, wantMacros: []string{"//old:macro"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := config.New()
			l := NewLanguage()
			l.Configure(c, "", nil)
			getConfig(c).cargo = index
			rc := &resolve.Configurer{}
			rc.RegisterFlags(nil, "", c)
			f := rule.EmptyFile("BUILD.bazel", "")
			if tc.override != "" {
				f.Directives = []rule.Directive{{Key: "resolve", Value: "rs " + tc.imported + " " + tc.override}}
			}
			rc.Configure(c, "", f)
			ix := resolve.NewRuleIndex(func(*rule.Rule, string) resolve.Resolver { return l })
			if tc.internal != "" {
				ix.AddRule(c, rule.NewRule("rust_library", tc.internal), rule.EmptyFile("internal/BUILD.bazel", "internal"))
			}
			ix.Finish()
			extra := ""
			if tc.aliasExpr != "" {
				extra = ", aliases = " + tc.aliasExpr
			}
			parsed, err := rule.LoadData("BUILD.bazel", "", []byte(`rust_library(name="app", deps=["//old:dep"], proc_macro_deps=["//old:macro"]`+extra+`)`))
			if err != nil {
				t.Fatal(err)
			}
			r := parsed.Rules[0]
			l.Resolve(c, ix, nil, r, importData{names: []string{tc.imported}}, label.New("", tc.pkg, "app"))
			if got := r.AttrStrings("deps"); !reflect.DeepEqual(got, tc.wantDeps) {
				t.Fatalf("deps = %v, want %v", got, tc.wantDeps)
			}
			if got := r.AttrStrings("proc_macro_deps"); !reflect.DeepEqual(got, tc.wantMacros) {
				t.Fatalf("macros = %v, want %v", got, tc.wantMacros)
			}
			if tc.wantAlias {
				aliases, _ := crateAliases(r)
				if aliases["@crates//:serde-1.0.0"] != "renamed" {
					t.Fatalf("aliases = %v", aliases)
				}
			}
			if tc.aliasExpr == "ALIASES" {
				if _, editable := crateAliases(r); editable {
					t.Fatal("computed aliases overwritten")
				}
			}
		})
	}
}

func TestRejectIncompleteCargoMetadata(t *testing.T) {
	for _, data := range []string{`{`, `{"version":2,"resolve":{"nodes":[]}}`, `{"version":1,"resolve":null}`} {
		file := filepath.Join(t.TempDir(), "metadata.json")
		if err := os.WriteFile(file, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := loadCargoIndex("@crates", file); err == nil {
			t.Fatalf("accepted %s", data)
		}
	}
}
