package rs

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/bazelbuild/bazel-gazelle/config"
	"github.com/bazelbuild/bazel-gazelle/label"
	"github.com/bazelbuild/bazel-gazelle/language"
	"github.com/bazelbuild/bazel-gazelle/resolve"
	"github.com/bazelbuild/bazel-gazelle/rule"
)

func TestManifestFreeDependencies(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"api/src/lib.rs":    "pub fn answer() -> u32 { 42 }",
		"app/src/main.rs":   "use api::answer; mod helper; fn main() { helper::run(); }",
		"app/src/helper.rs": "pub fn run() { let _ = api::answer(); }",
	}
	for file, text := range files {
		file = filepath.Join(root, file)
		if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	l := NewLanguage()
	ix := resolve.NewRuleIndex(func(*rule.Rule, string) resolve.Resolver { return l })
	configs := map[string]*config.Config{}
	results := map[string]language.GenerateResult{}
	for _, pkg := range []string{"api", "app"} {
		c := config.New()
		c.RepoRoot = root
		rc := &resolve.Configurer{}
		rc.RegisterFlags(nil, "", c)
		l.Configure(c, pkg, nil)
		configs[pkg] = c
		result := l.GenerateRules(language.GenerateArgs{Config: c, Dir: filepath.Join(root, pkg), Rel: pkg})
		if len(result.Gen) != 1 {
			t.Fatalf("%s: generated %d rules", pkg, len(result.Gen))
		}
		results[pkg] = result
		file := rule.EmptyFile(filepath.Join(root, pkg, "BUILD.bazel"), pkg)
		ix.AddRule(c, result.Gen[0], file)
	}
	ix.Finish()
	r := results["app"].Gen[0]
	l.Resolve(configs["app"], ix, nil, r, results["app"].Imports[0], label.New("", "app", "app"))
	if got := r.AttrStrings("deps"); !reflect.DeepEqual(got, []string{"//api"}) {
		t.Fatalf("deps=%v, want //api", got)
	}
	if got := r.AttrStrings("srcs"); !reflect.DeepEqual(got, []string{"src/helper.rs", "src/main.rs"}) {
		t.Fatalf("srcs=%v", got)
	}
	child := configs["app"].Clone()
	l.Configure(child, "app/src", nil)
	if got := l.GenerateRules(language.GenerateArgs{Config: child, Dir: filepath.Join(root, "app/src"), Rel: "app/src"}); len(got.Gen) != 0 {
		t.Fatalf("generated duplicate child crate: %v", got.Gen)
	}
}

func TestResolutionAmbiguityOverridesAndProcMacros(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		ambiguous, override, macro bool
		want                       string
	}{
		{name: "ambiguous", ambiguous: true, want: "//existing:dep"},
		{name: "override", ambiguous: true, override: true, want: "//one:util"},
		{name: "proc_macro", macro: true, want: "//one:util"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := config.New()
			l := NewLanguage()
			l.Configure(c, "", nil)
			rc := &resolve.Configurer{}
			rc.RegisterFlags(nil, "", c)
			file := rule.EmptyFile("BUILD.bazel", "")
			if tc.override {
				file.Directives = []rule.Directive{{Key: "resolve", Value: "rs util //one:util"}}
			}
			rc.Configure(c, "", file)
			ix := resolve.NewRuleIndex(func(*rule.Rule, string) resolve.Resolver { return l })
			kind := "rust_library"
			if tc.macro {
				kind = "rust_proc_macro"
			}
			lib := rule.NewRule(kind, "util")
			ix.AddRule(c, lib, rule.EmptyFile("one/BUILD.bazel", "one"))
			if tc.ambiguous {
				ix.AddRule(c, rule.NewRule("rust_library", "util"), rule.EmptyFile("two/BUILD.bazel", "two"))
			}
			ix.Finish()
			r := rule.NewRule("rust_library", "app")
			r.SetAttr("deps", []string{"//existing:dep"})
			l.Resolve(c, ix, nil, r, importData{names: []string{"util"}}, label.New("", "app", "app"))
			attr := "deps"
			if tc.macro {
				attr = "proc_macro_deps"
			}
			got := r.AttrStrings(attr)
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("unexpected deps %v", got)
				}
			} else if !reflect.DeepEqual(got, []string{tc.want}) {
				t.Fatalf("%s=%v", attr, got)
			}
		})
	}
}

func TestExistingRulesAndFailedGraphs(t *testing.T) {
	for _, tc := range []struct {
		name, build, source               string
		nestedPackage, wantRule, preserve bool
	}{
		{name: "computed_sources", build: `rust_library(name = "custom", crate_name = "pkg", crate_root = "lib.rs", srcs = glob(["*.rs"]))`, source: "pub fn f() {}"},
		{name: "computed_deps", build: `rust_library(name = "custom", crate_root = "lib.rs", srcs = ["lib.rs"], deps = select({"//conditions:default": []}))`, source: "pub fn f() {}", wantRule: true, preserve: true},
		{name: "missing_module", build: `rust_library(name = "custom", crate_root = "lib.rs", srcs = ["lib.rs"], deps = ["//old"])`, source: "mod missing;"},
		{name: "package_boundary", build: `rust_library(name = "custom", crate_root = "lib.rs", srcs = ["lib.rs"])`, source: "mod nested;", nestedPackage: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "lib.rs"), []byte(tc.source), 0644); err != nil {
				t.Fatal(err)
			}
			if tc.nestedPackage {
				if err := os.MkdirAll(filepath.Join(dir, "nested"), 0755); err != nil {
					t.Fatal(err)
				}
				for _, file := range []string{"mod.rs", "BUILD.bazel"} {
					if err := os.WriteFile(filepath.Join(dir, "nested", file), nil, 0644); err != nil {
						t.Fatal(err)
					}
				}
			}
			file, err := rule.LoadData(filepath.Join(dir, "BUILD.bazel"), "", []byte(tc.build))
			if err != nil {
				t.Fatal(err)
			}
			c := config.New()
			c.RepoRoot = dir
			l := NewLanguage()
			l.Configure(c, "", file)
			got := l.GenerateRules(language.GenerateArgs{Config: c, Dir: dir, File: file})
			if !tc.wantRule {
				if len(got.Gen) != 0 {
					t.Fatalf("unexpected rules: %v", got.Gen)
				}
				return
			}
			if len(got.Gen) != 1 || got.Gen[0].Name() != "custom" {
				t.Fatalf("lost existing target ownership: %v", got.Gen)
			}
			if got.Imports[0].(importData).preserve != tc.preserve {
				t.Fatal("computed dependencies were not preserved")
			}
		})
	}
}

func TestStandaloneTestsAndManualOwnerTests(t *testing.T) {
	dir := t.TempDir()
	for name, source := range map[string]string{
		"lib.rs":      "#[cfg(test)] mod tests { #[test] fn run() { inline_support::check(); } }",
		"api.test.rs": "#[test] fn run() { test_support::check(); }",
		"extra.rs":    "#[test] fn run() {}",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(source), 0644); err != nil {
			t.Fatal(err)
		}
	}
	file, err := rule.LoadData(filepath.Join(dir, "BUILD.bazel"), "", []byte(`
rust_library(name = "lib", crate_root = "lib.rs", srcs = ["lib.rs"])
rust_test(name = "manual", crate = ":lib", srcs = ["extra.rs"], deps = ["//manual:dep"])
`))
	if err != nil {
		t.Fatal(err)
	}
	c := config.New()
	c.RepoRoot = dir
	l := NewLanguage()
	l.Configure(c, "", file)
	result := l.GenerateRules(language.GenerateArgs{Config: c, Dir: dir, File: file})
	names := []string{}
	for i, r := range result.Gen {
		names = append(names, r.Name())
		if r.Name() == "api_test" {
			if r.AttrString("crate_root") != "api.test.rs" || r.Attr("crate") != nil {
				t.Fatalf("not a standalone test: %v", r)
			}
			if got := result.Imports[i].(importData).names; !reflect.DeepEqual(got, []string{"test_support"}) {
				t.Fatalf("imports = %v", got)
			}
		}
	}
	if !reflect.DeepEqual(names, []string{"api_test", "lib"}) {
		t.Fatalf("generated %v; want only standalone test and library", names)
	}
}

func TestNestedBuildCanOwnAnIndependentCrate(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"", "models"} {
		dir := filepath.Join(root, rel, "src")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "lib.rs"), []byte("pub fn run() {}"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	l := NewLanguage()
	parent := config.New()
	parent.RepoRoot = root
	l.Configure(parent, "", nil)
	child := parent.Clone()
	file, err := rule.LoadData(filepath.Join(root, "models/BUILD.bazel"), "models", []byte(`rust_library(name = "models", crate_root = "src/lib.rs", srcs = ["src/lib.rs"])`))
	if err != nil {
		t.Fatal(err)
	}
	l.Configure(child, "models", file)
	got := l.GenerateRules(language.GenerateArgs{Config: child, Dir: filepath.Join(root, "models"), Rel: "models", File: file})
	if len(got.Gen) != 1 || got.Gen[0].Name() != "models" {
		t.Fatalf("nested crate was suppressed: %v", got.Gen)
	}
	if getConfig(parent).owner != "" || getConfig(child).owner != "models" {
		t.Fatal("child ownership leaked into its parent")
	}
}

func TestExcludedCrateRoots(t *testing.T) {
	root := t.TempDir()
	for _, file := range []string{"pkg/src/lib.rs", "pkg/src/bin/tool.rs", "pkg/tests/privileged.rs", "pkg/tests/keep.rs"} {
		full := filepath.Join(root, file)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("pub fn run() {}"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	c := config.New()
	c.RepoRoot = root
	l := NewLanguage()
	parent, err := rule.LoadData(filepath.Join(root, "BUILD.bazel"), "", []byte("# gazelle:exclude pkg/src/bin\n"))
	if err != nil {
		t.Fatal(err)
	}
	l.Configure(c, "", parent)
	child, err := rule.LoadData(filepath.Join(root, "pkg/BUILD.bazel"), "pkg", []byte("# gazelle:exclude tests/priv*.rs\n"))
	if err != nil {
		t.Fatal(err)
	}
	l.Configure(c, "pkg", child)
	result := l.GenerateRules(language.GenerateArgs{Config: c, Dir: filepath.Join(root, "pkg"), Rel: "pkg", File: child})
	var roots []string
	for _, r := range result.Gen {
		roots = append(roots, r.AttrString("crate_root"))
	}
	if !reflect.DeepEqual(roots, []string{"src/lib.rs", "tests/keep.rs"}) {
		t.Fatalf("roots=%v", roots)
	}
}

func TestResolutionRetainsExplicitCrateVariant(t *testing.T) {
	c := config.New()
	c.RepoRoot = t.TempDir()
	l := NewLanguage()
	rc := &resolve.Configurer{}
	rc.RegisterFlags(nil, "", c)
	l.Configure(c, "", nil)
	ix := resolve.NewRuleIndex(func(*rule.Rule, string) resolve.Resolver { return l })
	file := rule.EmptyFile(filepath.Join(c.RepoRoot, "BUILD.bazel"), "")
	for _, name := range []string{"api", "api_minimal"} {
		r := rule.NewRule("rust_library", name)
		r.SetAttr("crate_name", "api")
		ix.AddRule(c, r, file)
	}
	ix.Finish()
	for _, attr := range []string{"deps", "crate"} {
		r := rule.NewRule("rust_test", "consumer")
		if attr == "deps" {
			r.SetAttr(attr, []string{":api_minimal"})
		} else {
			r.SetAttr(attr, ":api_minimal")
		}
		l.Resolve(c, ix, nil, r, importData{names: []string{"api"}}, label.New("", "", "consumer"))
		if got := r.AttrStrings("deps"); !reflect.DeepEqual(got, []string{":api_minimal"}) {
			t.Fatalf("%s: deps=%v", attr, got)
		}
	}
	// Multiple explicit candidates remain ambiguous and must preserve unrelated deps.
	r := rule.NewRule("rust_test", "ambiguous")
	want := []string{":api", ":api_minimal", ":retained"}
	r.SetAttr("deps", want)
	l.Resolve(c, ix, nil, r, importData{names: []string{"api"}}, label.New("", "", "ambiguous"))
	if got := r.AttrStrings("deps"); !reflect.DeepEqual(got, want) {
		t.Fatalf("ambiguous deps=%v", got)
	}
}
