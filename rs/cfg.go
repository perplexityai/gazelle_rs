package rs

import (
	"fmt"
	"sort"
	"strings"

	"github.com/bazelbuild/bazel-gazelle/label"
	"github.com/bazelbuild/bazel-gazelle/rule"
	"github.com/bazelbuild/buildtools/build"
	pb "github.com/perplexityai/gazelle_rs/rs/proto"
)

// Rust and Bazel use different names for a few canonical operating systems.
func defaultCfgLabels() map[string]string {
	out := map[string]string{}
	for _, os := range strings.Fields("aix android emscripten freebsd fuchsia haiku ios linux netbsd none openbsd tvos uefi visionos vxworks wasi watchos windows") {
		out[os] = "@platforms//os:" + os
	}
	out["macos"] = "@platforms//os:osx"
	out["nto"] = "@platforms//os:qnx"
	return out
}

func cfgDirective(cfg *rustConfig, value string) error {
	args := strings.Fields(value)
	if len(args) != 2 || !strings.HasPrefix(args[0], "target_os=") || strings.TrimPrefix(args[0], "target_os=") == "" {
		return fmt.Errorf("expected rust_cfg target_os=<value> <absolute constraint label>")
	}
	os := strings.TrimPrefix(args[0], "target_os=")
	parsed, err := label.Parse(args[1])
	if err != nil || parsed.Relative || !strings.Contains(args[1], "//") {
		return fmt.Errorf("rust_cfg requires an absolute constraint label, got %q", args[1])
	}
	for other, existing := range cfg.cfgLabels {
		if other != os && existing == args[1] {
			return fmt.Errorf("rust_cfg: %s and %s cannot share OS constraint %s", other, os, args[1])
		}
	}
	cfg.cfgLabels[os] = args[1]
	return nil
}

type truth uint8

const (
	unknown truth = iota
	no
	yes
)

// An unrecognized predicate is possibly true, even below not(). This retains
// dependencies conservatively without discarding a known enclosing OS guard.
func evalCfg(c *pb.Cfg, os string, testing bool, unsupported map[string]bool) truth {
	if c == nil {
		return yes
	}
	if c.Op != "" {
		switch c.Op {
		case "all", "any":
			result := yes
			if c.Op == "any" {
				result = no
			}
			for _, arg := range c.Args {
				value := evalCfg(arg, os, testing, unsupported)
				if c.Op == "all" && value == no {
					return no
				}
				if c.Op == "any" && value == yes {
					return yes
				}
				if value == unknown {
					result = unknown
				}
			}
			return result
		case "not":
			if len(c.Args) == 1 {
				switch evalCfg(c.Args[0], os, testing, unsupported) {
				case yes:
					return no
				case no:
					return yes
				default:
					return unknown
				}
			}
		}
	} else {
		switch c.Key {
		case "target_os":
			if c.Value == os {
				return yes
			}
			return no
		case "test":
			if testing {
				return yes
			}
			return no
		}
	}
	name := c.Key
	if c.Op != "" {
		name = c.Op
	}
	if name == "" {
		name = "unrecognized cfg"
	}
	unsupported[name] = true
	return unknown
}

func cfgOperatingSystems(c *pb.Cfg, systems map[string]bool) {
	if c == nil {
		return
	}
	if c.Key == "target_os" {
		systems[c.Value] = true
	}
	for _, arg := range c.Args {
		cfgOperatingSystems(arg, systems)
	}
}

// Only maintain expressions whose branch meanings are explicit in the mapping.
// In particular, arbitrary functions and project config_settings stay user-owned.
func editableCfgDeps(expr build.Expr, labels map[string]string) bool {
	if expr == nil {
		return true
	}
	switch e := expr.(type) {
	case *build.ListExpr:
		for _, item := range e.List {
			if _, ok := item.(*build.StringExpr); !ok {
				return false
			}
		}
		return true
	case *build.BinaryExpr:
		if e.Op != "+" {
			return false
		}
		_, leftList := e.X.(*build.ListExpr)
		_, rightList := e.Y.(*build.ListExpr)
		_, leftSelect := e.X.(*build.CallExpr)
		_, rightSelect := e.Y.(*build.CallExpr)
		return ((leftList && rightSelect) || (leftSelect && rightList)) && editableCfgDeps(e.X, labels) && editableCfgDeps(e.Y, labels)

	case *build.CallExpr:
		callee, ok := e.X.(*build.Ident)
		if !ok || callee.Name != "select" || len(e.List) != 1 {
			return false
		}
		dict, ok := e.List[0].(*build.DictExpr)
		if !ok {
			return false
		}
		// A default-only select has no meaningful platform information; keep the
		// existing convention that computed dependency expressions are user-owned.
		hasOS := false
		for _, entry := range dict.List {
			key, ok := entry.Key.(*build.StringExpr)
			if !ok {
				return false
			}
			if key.Value != "//conditions:default" {
				known := false
				for _, value := range labels {
					known = known || value == key.Value
				}
				if !known {
					return false
				}
				hasOS = true
			}
			list, ok := entry.Value.(*build.ListExpr)
			if !ok || !editableCfgDeps(list, labels) {
				return false
			}
		}
		return hasOS
	}
	return false
}

// The default Gazelle merger interprets only some select keys. Own the merge
// for expressions we understand, retaining existing comments and # keep items.
type cfgDependencyValue struct{ expr build.Expr }

func (v cfgDependencyValue) BzlExpr() build.Expr { return v.expr }
func (v cfgDependencyValue) Merge(old build.Expr) build.Expr {
	return mergeCfgComments(v.expr, old)
}

func cfgLists(expr build.Expr) map[string]*build.ListExpr {
	out := map[string]*build.ListExpr{}
	var walk func(build.Expr)
	walk = func(expr build.Expr) {
		switch e := expr.(type) {
		case *build.ListExpr:
			out[""] = e
		case *build.BinaryExpr:
			walk(e.X)
			walk(e.Y)
		case *build.CallExpr:
			if len(e.List) == 1 {
				if d, ok := e.List[0].(*build.DictExpr); ok {
					for _, kv := range d.List {
						if key, ok := kv.Key.(*build.StringExpr); ok {
							if list, ok := kv.Value.(*build.ListExpr); ok {
								out[key.Value] = list
							}
						}
					}
				}
			}
		}
	}
	walk(expr)
	return out
}
func cfgSelect(expr build.Expr) *build.CallExpr {
	switch e := expr.(type) {
	case *build.CallExpr:
		return e
	case *build.BinaryExpr:
		if call := cfgSelect(e.X); call != nil {
			return call
		}
		return cfgSelect(e.Y)
	}
	return nil
}
func mergeCfgComments(expr, old build.Expr) build.Expr {
	if old != nil {
		*expr.Comment() = *old.Comment()
	}
	if current, previous := cfgSelect(expr), cfgSelect(old); current != nil && previous != nil {
		current.Comments = previous.Comments
		dict := current.List[0].(*build.DictExpr)
		before := previous.List[0].(*build.DictExpr)
		dict.Comments = before.Comments
		entries := map[string]*build.KeyValueExpr{}
		for _, kv := range before.List {
			entries[kv.Key.(*build.StringExpr).Value] = kv
		}
		for _, kv := range dict.List {
			if prior := entries[kv.Key.(*build.StringExpr).Value]; prior != nil {
				kv.Comments = prior.Comments
				kv.Key = prior.Key
			}
		}
	}
	fresh, previous := cfgLists(expr), cfgLists(old)
	for key, list := range fresh {
		before := previous[key]
		if before == nil {
			continue
		}
		if rule.ShouldKeep(before) {
			*list = *before
			continue
		}
		nodes := map[string]*build.StringExpr{}
		for _, item := range before.List {
			if s, ok := item.(*build.StringExpr); ok {
				nodes[s.Value] = s
			}
		}
		used := map[string]bool{}
		for i, item := range list.List {
			if s, ok := item.(*build.StringExpr); ok {
				used[s.Value] = true
				if existing := nodes[s.Value]; existing != nil {
					list.List[i] = existing
				}
			}
		}
		for value, item := range nodes {
			if !used[value] && rule.ShouldKeep(item) {
				list.List = append(list.List, item)
			}
		}
		sort.SliceStable(list.List, func(i, j int) bool {
			return list.List[i].(*build.StringExpr).Value < list.List[j].(*build.StringExpr).Value
		})
		list.Comments = before.Comments
	}
	return expr
}

func conditionalDeps(values map[string][]*pb.Cfg, labels map[string]string, testing bool, unsupported map[string]bool, branchKeeps map[string]bool) (build.Expr, error) {
	systems := map[string]bool{}
	for _, conditions := range values {
		for _, condition := range conditions {
			cfgOperatingSystems(condition, systems)
		}
	}
	var names []string
	for os := range systems {
		if labels[os] == "" {
			return nil, fmt.Errorf("no Bazel constraint for target_os=%s; add # gazelle:rust_cfg target_os=%s <label>", os, os)
		}
		names = append(names, os)
	}
	sort.Strings(names)
	branches := map[string][]string{}
	var common []string
	var deps []string
	for dep := range values {
		deps = append(deps, dep)
	}
	sort.Strings(deps)
	for _, dep := range deps {
		matches := func(os string) bool {
			for _, condition := range values[dep] {
				if evalCfg(condition, os, testing, unsupported) != no {
					return true
				}
			}
			return false
		}
		defaultMatch := matches("")
		everywhere := defaultMatch
		for _, os := range names {
			everywhere = everywhere && matches(os)
		}
		if everywhere && !branchKeeps[dep] {
			common = append(common, dep)
			continue
		}
		if defaultMatch {
			branches["//conditions:default"] = append(branches["//conditions:default"], dep)
		}
		for _, os := range names {
			if matches(os) {
				branches[labels[os]] = append(branches[labels[os]], dep)
			}
		}
	}
	if len(branches) == 0 {
		if len(common) == 0 {
			return nil, nil
		}
		return rule.ExprFromValue(common), nil
	}
	// Empty branches are significant for not(target_os=...) and the default.
	for _, os := range names {
		if branches[labels[os]] == nil {
			branches[labels[os]] = []string{}
		}
	}
	if branches["//conditions:default"] == nil {
		branches["//conditions:default"] = []string{}
	}
	var keys []string
	for key := range branches {
		if key != "//conditions:default" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	keys = append(keys, "//conditions:default")
	dict := &build.DictExpr{ForceMultiLine: true}
	for _, key := range keys {
		list := rule.ExprFromValue(branches[key]).(*build.ListExpr)
		if len(list.List) == 0 {
			list.ForceMultiLine = false
		}
		dict.List = append(dict.List, &build.KeyValueExpr{Key: &build.StringExpr{Value: key}, Value: list})
	}
	expr := &build.CallExpr{X: &build.Ident{Name: "select"}, List: []build.Expr{dict}}

	if len(common) == 0 {
		return expr, nil
	}
	return &build.BinaryExpr{X: rule.ExprFromValue(common), Op: "+", Y: expr}, nil
}

func hasOSConditions(conditions map[string][]*pb.Cfg) bool {
	systems := map[string]bool{}
	for _, values := range conditions {
		for _, condition := range values {
			cfgOperatingSystems(condition, systems)
		}
	}
	return len(systems) > 0
}
func dependencyLabels(r *rule.Rule) []string {
	var out []string
	for _, attr := range []string{"deps", "proc_macro_deps"} {
		for _, list := range cfgLists(r.Attr(attr)) {
			for _, item := range list.List {
				if value, ok := item.(*build.StringExpr); ok {
					out = append(out, value.Value)
				}
			}
		}
	}
	return out
}

// Translate only explicit keeps back into guards before rendering, so retained
// branches still receive new imports instead of freezing the whole expression.
func keepCfgDeps(values map[string][]*pb.Cfg, expr build.Expr, labels map[string]string) map[string]bool {
	branchKeeps := map[string]bool{}
	lists := cfgLists(expr)
	var selected []*pb.Cfg
	reverse := map[string]string{}
	for os, label := range labels {
		reverse[label] = os
	}
	for key := range lists {
		if os, ok := reverse[key]; ok {
			selected = append(selected, &pb.Cfg{Key: "target_os", Value: os})
		}
	}
	for key, list := range lists {
		var condition *pb.Cfg
		if key == "//conditions:default" {
			condition = &pb.Cfg{Op: "not", Args: []*pb.Cfg{{Op: "any", Args: selected}}}
		} else if key != "" {
			condition = &pb.Cfg{Key: "target_os", Value: reverse[key]}
		}
		for _, item := range list.List {
			if rule.ShouldKeep(list) || rule.ShouldKeep(item) {
				if value, ok := item.(*build.StringExpr); ok {
					values[value.Value] = append(values[value.Value], condition)
					if key != "" {
						branchKeeps[value.Value] = true
					}
				}
			}
		}
	}
	return branchKeeps
}
