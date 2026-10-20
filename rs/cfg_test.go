package rs

import (
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/bazelbuild/bazel-gazelle/config"
	"github.com/bazelbuild/bazel-gazelle/rule"
	"github.com/bazelbuild/buildtools/build"
	pb "github.com/perplexityai/gazelle_rs/rs/proto"
)

func osCfg(os string) *pb.Cfg                  { return &pb.Cfg{Key: "target_os", Value: os} }
func cfgOp(op string, args ...*pb.Cfg) *pb.Cfg { return &pb.Cfg{Op: op, Args: args} }

// Interpret the emitted BUILD expression as Bazel does on one OS. This checks
// configuration behavior as well as the absence of duplicate dependencies.
func configuredDeps(t *testing.T, expr build.Expr, os string, labels map[string]string) []string {
	t.Helper()
	var out []string
	lists := cfgLists(expr)
	appendList := func(list *build.ListExpr) {
		if list != nil {
			for _, item := range list.List {
				out = append(out, item.(*build.StringExpr).Value)
			}
		}
	}
	appendList(lists[""])
	if selected := lists[labels[os]]; selected != nil {
		appendList(selected)
	} else {
		appendList(lists["//conditions:default"])
	}
	sort.Strings(out)
	for i := 1; i < len(out); i++ {
		if out[i] == out[i-1] {
			t.Fatalf("duplicate dependency %s for %s", out[i], os)
		}
	}
	return out
}
func TestConditionalDependencyConfigurations(t *testing.T) {
	labels := defaultCfgLabels()
	values := map[string][]*pb.Cfg{
		"//common":          {osCfg("windows"), nil},
		"//union":           {osCfg("windows"), osCfg("macos")},
		"//nonwindows":      {cfgOp("not", osCfg("windows"))},
		"//impossible":      {cfgOp("all", osCfg("windows"), osCfg("macos"))},
		"//feature":         {cfgOp("all", osCfg("windows"), &pb.Cfg{Key: "feature", Value: "desktop"})},
		"//unknown_negated": {cfgOp("not", &pb.Cfg{Key: "feature", Value: "desktop"})},
	}
	unknowns := map[string]bool{}
	expr, err := conditionalDeps(values, labels, false, unknowns, nil)
	if err != nil {
		t.Fatal(err)
	}
	for os, want := range map[string][]string{
		"windows": {"//common", "//feature", "//union", "//unknown_negated"},
		"macos":   {"//common", "//nonwindows", "//union", "//unknown_negated"},
		"linux":   {"//common", "//nonwindows", "//unknown_negated"},
	} {
		if got := configuredDeps(t, expr, os, labels); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %v, want %v", os, got, want)
		}
	}
	if !unknowns["feature"] {
		t.Fatal("unsupported feature predicate was not reported")
	}
}
func TestCfgTestHarnessAndMissingMapping(t *testing.T) {
	values := map[string][]*pb.Cfg{"//test": {{Key: "test"}}}
	for _, testing := range []bool{false, true} {
		expr, err := conditionalDeps(values, defaultCfgLabels(), testing, map[string]bool{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		got := configuredDeps(t, expr, "linux", defaultCfgLabels())
		if (len(got) == 1) != testing {
			t.Fatalf("testing=%t: deps=%v", testing, got)
		}
	}
	if _, err := conditionalDeps(map[string][]*pb.Cfg{"//custom": {osCfg("custom")}}, defaultCfgLabels(), false, map[string]bool{}, nil); err == nil {
		t.Fatal("unmapped OS must not silently lose its dependency")
	}
}
func parseDeps(t *testing.T, expression string) build.Expr {
	t.Helper()
	file, err := rule.LoadData("BUILD.bazel", "", []byte("rust_library(name=\"lib\",deps="+expression+")"))
	if err != nil {
		t.Fatal(err)
	}
	return file.Rules[0].Attr("deps")
}
func TestCfgDependencyOwnership(t *testing.T) {
	for _, tc := range []struct {
		expr string
		want bool
	}{
		{`["//common"]`, true},
		{`select({"@platforms//os:windows":["//win"],"//conditions:default":[]})`, true},
		{`["//common"] + select({"@platforms//os:windows":[],"//conditions:default":[]})`, true},
		{`["//one"] + ["//two"] + select({"@platforms//os:windows":[],"//conditions:default":[]})`, false},
		{`select({"//custom:flag":["//win"],"//conditions:default":[]})`, false},
		{`select({"//conditions:default":["//manual"]})`, false},
		{`all_crate_deps()`, false},
	} {
		if got := editableCfgDeps(parseDeps(t, tc.expr), defaultCfgLabels()); got != tc.want {
			t.Errorf("%s: editable=%t", tc.expr, got)
		}
	}
}
func TestCfgKeepsSurviveWithNewImports(t *testing.T) {
	old := parseDeps(t, `select({
    # Retain this Windows branch.
    "@platforms//os:windows": [
        "//generated:bindings", # keep
        "//stale:dep",
    ],
    "//conditions:default": [],
 })`)
	values := map[string][]*pb.Cfg{"//new:dep": {osCfg("linux")}}
	labels := defaultCfgLabels()
	keeps := keepCfgDeps(values, old, labels)
	expr, err := conditionalDeps(values, labels, false, map[string]bool{}, keeps)
	if err != nil {
		t.Fatal(err)
	}
	merged := cfgDependencyValue{expr}.Merge(old)
	for os, want := range map[string][]string{"windows": {"//generated:bindings"}, "linux": {"//new:dep"}, "macos": nil} {
		if got := configuredDeps(t, merged, os, labels); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %v, want %v", os, got, want)
		}
	}
	var branchComment bool
	for _, kv := range cfgSelect(merged).List[0].(*build.DictExpr).List {
		if kv.Key.(*build.StringExpr).Value == labels["windows"] {
			branchComment = len(kv.Comments.Before) > 0 || len(kv.Key.Comment().Before) > 0
		}
	}
	if !branchComment {
		t.Fatal("branch comment disappeared")
	}
	if !rule.ShouldKeep(cfgLists(merged)[labels["windows"]].List[0]) {
		t.Fatal("keep comment disappeared")
	}
}
func TestCfgDirectiveInheritance(t *testing.T) {
	root := t.TempDir()
	l := NewLanguage()
	parent := config.New()
	parent.RepoRoot = root
	file := rule.EmptyFile(filepath.Join(root, "BUILD.bazel"), "")
	file.Directives = []rule.Directive{{Key: "rust_cfg", Value: "target_os=windows //company/os:windows"}}
	l.Configure(parent, "", file)
	child := parent.Clone()
	l.Configure(child, "nested", nil)
	if got := getConfig(child).cfgLabels["windows"]; got != "//company/os:windows" {
		t.Fatalf("inherited constraint=%s", got)
	}
	local := rule.EmptyFile(filepath.Join(root, "nested", "BUILD.bazel"), "nested")
	local.Directives = []rule.Directive{{Key: "rust_cfg", Value: "target_os=windows //other/os:windows"}}
	l.Configure(child, "nested", local)
	if getConfig(parent).cfgLabels["windows"] != "//company/os:windows" {
		t.Fatal("local override mutated parent")
	}
	values := map[string][]*pb.Cfg{"//win": {osCfg("windows")}}
	expr, err := conditionalDeps(values, getConfig(child).cfgLabels, false, map[string]bool{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfgLists(expr)["//other/os:windows"] == nil {
		t.Fatal("configured custom constraint not emitted")
	}
	for _, value := range []string{"windows //bad:windows", "target_os=windows :relative"} {
		if cfgDirective(getConfig(child), value) == nil {
			t.Errorf("accepted invalid mapping %s", value)
		}
	}
	if err := cfgDirective(getConfig(parent), "target_os=linux //company/os:windows"); err == nil {
		t.Fatal("overlapping OS mapping accepted")
	}
}

func TestCfgBranchKeepsSurviveTemporaryUnconditionalUse(t *testing.T) {
	labels := defaultCfgLabels()
	old := parseDeps(t, `select({
  "@platforms//os:windows": ["//common:dep", # keep
  ],
  "//conditions:default": [],
 })`)
	generate := func(values map[string][]*pb.Cfg, previous build.Expr) build.Expr {
		keeps := keepCfgDeps(values, previous, labels)
		expr, err := conditionalDeps(values, labels, false, map[string]bool{}, keeps)
		if err != nil {
			t.Fatal(err)
		}
		return cfgDependencyValue{expr}.Merge(previous)
	}
	withImport := generate(map[string][]*pb.Cfg{"//common:dep": {nil}}, old)
	for _, os := range []string{"windows", "linux"} {
		if got := configuredDeps(t, withImport, os, labels); !reflect.DeepEqual(got, []string{"//common:dep"}) {
			t.Fatalf("%s: %v", os, got)
		}
	}
	withoutImport := generate(map[string][]*pb.Cfg{}, withImport)
	if got := configuredDeps(t, withoutImport, "windows", labels); !reflect.DeepEqual(got, []string{"//common:dep"}) {
		t.Fatalf("Windows keep was lost: %v", got)
	}
	if got := configuredDeps(t, withoutImport, "linux", labels); len(got) != 0 {
		t.Fatalf("Windows keep became unconditional: %v", got)
	}
}
