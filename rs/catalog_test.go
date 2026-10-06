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

func TestBazelCatalogResolution(t *testing.T) {
	file := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(file, []byte(`{"version":1,"crates":[
 {"name":"wire","label":"@vendor//:wire-1","aliases":["@vendor//:wire"]},
 {"name":"wire","label":"@vendor//:wire-2"},
 {"name":"derive","label":"//third_party:derive","proc_macro":true}
 ]}`), 0644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, source, imported string
		deps, macros           []string
	}{
		{name: "existing_version", source: `rust_library(name="app",deps=["@vendor//:wire-2"])`, imported: "wire", deps: []string{"@vendor//:wire-2"}},
		{name: "existing_unversioned", source: `rust_library(name="app",deps=["@vendor//:wire"])`, imported: "wire", deps: []string{"@vendor//:wire-1"}},
		{name: "renamed", source: `rust_library(name="app",aliases={"@vendor//:wire-2":"renamed"})`, imported: "renamed", deps: []string{"@vendor//:wire-2"}},
		{name: "macro", source: `rust_library(name="app")`, imported: "derive", macros: []string{"//third_party:derive"}},
		{name: "ambiguous", source: `rust_library(name="app",deps=["//existing:keep"])`, imported: "wire", deps: []string{"//existing:keep"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := config.New()
			c.RepoRoot = filepath.Dir(file)
			l := NewLanguage()
			f := rule.EmptyFile("BUILD.bazel", "")
			f.Directives = []rule.Directive{{Key: "rust_crate_catalog", Value: "catalog.json"}}
			l.Configure(c, "", f)
			rc := &resolve.Configurer{}
			rc.RegisterFlags(nil, "", c)
			rc.Configure(c, "", rule.EmptyFile("BUILD.bazel", ""))
			ix := resolve.NewRuleIndex(func(*rule.Rule, string) resolve.Resolver { return l })
			ix.Finish()
			parsed, err := rule.LoadData("BUILD.bazel", "", []byte(tc.source))
			if err != nil {
				t.Fatal(err)
			}
			r := parsed.Rules[0]
			l.Resolve(c, ix, nil, r, importData{names: []string{tc.imported}}, label.New("", "app", "app"))
			if !reflect.DeepEqual(r.AttrStrings("deps"), tc.deps) || !reflect.DeepEqual(r.AttrStrings("proc_macro_deps"), tc.macros) {
				t.Fatalf("got deps %v macros %v", r.AttrStrings("deps"), r.AttrStrings("proc_macro_deps"))
			}
		})
	}
}
func TestRejectInvalidCatalog(t *testing.T) {
	for _, data := range []string{
		`{"version":2,"crates":[]}`,
		`{"version":1,"crates":[]} {}`,
		`{"version":1,"crates":[{"name":"a","label":"@dep//:a","proc_macor":true}]}`,
		`{"version":1}`,
		`{"version":1,"crates":[{"name":"a","label":":relative"}]}`,
		`{"version":1,"crates":[{"name":"a","label":"@@canonical//:a"}]}`,
		`{"version":1,"crates":[{"name":"bad-name","label":"@dep//:a"}]}`,
		`{"version":1,"crates":[{"name":"a","label":"@dep//:a"},{"name":"b","label":"@dep//:a"}]}`,
	} {
		file := filepath.Join(t.TempDir(), "catalog.json")
		if err := os.WriteFile(file, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := loadCrateCatalog(file); err == nil {
			t.Fatalf("accepted invalid catalog %s", data)
		}
	}
}
