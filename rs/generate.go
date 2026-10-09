package rs

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/bazelbuild/bazel-gazelle/language"
	"github.com/bazelbuild/bazel-gazelle/rule"
	"github.com/bazelbuild/buildtools/build"
	"github.com/bmatcuk/doublestar/v4"
	pb "github.com/perplexityai/gazelle_rs/rs/proto"
)

type target struct {
	Name, Path string
	ProcMacro  bool `toml:"proc-macro"`
}
type manifest struct {
	Package struct {
		Name    string
		Edition interface{}
	}
	Lib  target
	Bin  []target
	Test []target
}
type plan struct {
	name, root, kind, edition string
	roots                     []string
	existing                  *rule.Rule
	autoTest                  bool
	owner                     *rule.Rule
}
type importData struct {
	names    []string
	preserve bool
}

func excludedRoot(cfg *rustConfig, rel, root string) bool {
	for file := path.Join(rel, filepath.ToSlash(root)); file != "."; file = path.Dir(file) {
		for _, pattern := range cfg.excludes {
			if matched, _ := doublestar.Match(pattern, file); matched {
				return true
			}
		}
	}
	return false
}

func exists(file string) bool { st, err := os.Stat(file); return err == nil && !st.IsDir() }
func literalList(r *rule.Rule, attr string) bool {
	expr := r.Attr(attr)
	if expr == nil {
		return true
	}
	list, ok := expr.(*build.ListExpr)
	if !ok {
		return false
	}
	for _, el := range list.List {
		if _, ok := el.(*build.StringExpr); !ok {
			return false
		}
	}
	return true
}

// Computed dependencies are user-owned. Gazelle's default list merger only
// recognizes a subset of select keys and cannot merge arbitrary calls.
type preservedDependency struct{ expr build.Expr }

func (v preservedDependency) BzlExpr() build.Expr { return v.expr }
func (v preservedDependency) Merge(other build.Expr) build.Expr {
	if other != nil {
		return other
	}
	return v.expr
}

// Seed existing resolution attributes so an incomplete extraction or a computed
// dependency expression cannot turn an early Resolve return into a deletion.
func seedResolveAttrs(dst, src *rule.Rule) {
	if src == nil {
		return
	}
	for _, attr := range []string{"deps", "proc_macro_deps"} {
		if value := src.Attr(attr); value != nil {
			if literalList(src, attr) {
				dst.SetAttr(attr, value)
			} else {
				dst.SetAttr(attr, preservedDependency{value})
			}
		}
	}
	if value := src.Attr("aliases"); value != nil {
		dst.SetAttr("aliases", aliasValue{value})
	}
}

func discover(args language.GenerateArgs) ([]plan, error) {
	cfg := getConfig(args.Config)
	name := crateName(filepath.Base(args.Dir))
	edition := cfg.edition
	var doc manifest
	cargo := filepath.Join(args.Dir, "Cargo.toml")
	hasManifest := exists(cargo)
	if hasManifest {
		if _, err := toml.DecodeFile(cargo, &doc); err != nil {
			return nil, err
		}
		if doc.Package.Name != "" {
			name = crateName(doc.Package.Name)
		}
		if e, ok := doc.Package.Edition.(string); ok {
			edition = e
		}
	}
	if cfg.crateName != "" {
		name = cfg.crateName
	}
	var plans []plan
	add := func(t target, kind, fallback, defaultRoot string, autoTest bool) {
		if t.Path == "" {
			t.Path = defaultRoot
		}
		if t.Name == "" {
			t.Name = fallback
		}
		if t.Path != "" && exists(filepath.Join(args.Dir, t.Path)) {
			if t.ProcMacro {
				kind = "rust_proc_macro"
			}
			plans = append(plans, plan{name: crateName(t.Name), root: filepath.Clean(t.Path), kind: kind, edition: edition, autoTest: autoTest})
		}
	}
	choose := func(paths ...string) string {
		for _, p := range paths {
			if exists(filepath.Join(args.Dir, p)) {
				return p
			}
		}
		return ""
	}
	add(doc.Lib, "rust_library", name, choose("src/lib.rs", "lib.rs"), false)
	binaryName := name
	if len(plans) > 0 {
		binaryName += "_bin"
	}
	if len(doc.Bin) > 0 {
		for _, t := range doc.Bin {
			add(t, "rust_binary", binaryName, "src/main.rs", false)
		}
	} else {
		add(target{}, "rust_binary", binaryName, choose("src/main.rs", "main.rs"), false)
	}
	for _, file := range args.RegularFiles {
		if strings.HasSuffix(file, ".test.rs") {
			add(target{Path: file}, "rust_test", name+"_test", file, true)
			continue
		}
		if !hasManifest {
			continue
		}
		// Cargo's implicit binaries apply only to manifest-backed packages.
		for _, pattern := range []string{"src/bin/*.rs", "src/bin/*/main.rs"} {
			matched, _ := path.Match(pattern, filepath.ToSlash(file))
			if !matched {
				continue
			}
			n := strings.TrimSuffix(filepath.Base(file), ".rs")
			if n == "main" {
				n = filepath.Base(filepath.Dir(file))
			}
			add(target{Name: n, Path: file}, "rust_binary", n, file, false)
		}
	}
	for _, t := range doc.Test {
		for i := len(plans) - 1; i >= 0; i-- {
			if plans[i].autoTest && plans[i].root == t.Path {
				plans = append(plans[:i], plans[i+1:]...)
			}
		}
		add(t, "rust_test", t.Name+"_test", "", false)
	}
	// Explicit crate roots and names take precedence over discovery.
	if args.File != nil {
		for _, r := range args.File.Rules {
			kind := baseKind(args.Config, r.Kind())
			if kind != "rust_library" && kind != "rust_proc_macro" && kind != "rust_binary" && kind != "rust_test" {
				continue
			}
			// Owner-based tests remain manually maintained, including additional sources.
			if kind == "rust_test" && r.Attr("crate") != nil {
				continue
			}
			root := r.AttrString("crate_root")
			var roots []string
			if kind == "rust_test" && root == "" && literalList(r, "srcs") {
				roots = r.AttrStrings("srcs")
				for _, source := range roots {
					if !strings.HasSuffix(source, ".test.rs") || strings.Contains(source, ":") {
						roots = nil
						break
					}
				}
			}
			if len(roots) > 0 {
				aggregateEdition := edition
				if e := r.AttrString("edition"); e != "" {
					aggregateEdition = e
				}
				plans = append(plans, plan{name: r.Name(), root: "@aggregate:" + r.Name(), roots: roots, kind: kind, edition: aggregateEdition, existing: r})
				continue
			}
			if root == "" {
				for _, src := range r.AttrStrings("srcs") {
					if filepath.Base(src) == "lib.rs" || filepath.Base(src) == "main.rs" {
						root = src
						break
					}
				}
				if root == "" && len(r.AttrStrings("srcs")) == 1 {
					root = r.AttrStrings("srcs")[0]
				}
			}
			for i := len(plans) - 1; i >= 0; i-- {
				if plans[i].existing == nil && ((plans[i].root == root && plans[i].kind == kind) || plans[i].name == r.Name()) {
					plans = append(plans[:i], plans[i+1:]...)
				}
			}
			if root != "" {
				existingEdition := edition
				if e := r.AttrString("edition"); e != "" {
					existingEdition = e
				}
				plans = append(plans, plan{name: r.Name(), root: filepath.Clean(root), kind: kind, edition: existingEdition, existing: r})
			}
		}
	}
	seenRoots := map[string]bool{}
	var out []plan
	for _, p := range plans {
		if excludedRoot(cfg, args.Rel, p.root) {
			continue
		}
		if p.existing == nil && seenRoots[p.kind+":"+p.root] {
			continue
		}
		seenRoots[p.kind+":"+p.root] = true
		out = append(out, p)
	}
	return out, nil
}
func extractPlan(dir string, p plan) (*pb.CrateResult, error) {
	roots := []string{filepath.Join(dir, p.root)}
	if len(p.roots) > 0 {
		roots = nil
		for _, source := range p.roots {
			roots = append(roots, filepath.Join(dir, source))
		}
	}
	facts, err := extract(roots)
	if err != nil {
		return nil, err
	}
	merged := &pb.CrateResult{}
	for _, fact := range facts {
		merged.Sources = append(merged.Sources, fact.Sources...)
		merged.TestSources = append(merged.TestSources, fact.TestSources...)
		merged.Imports = append(merged.Imports, fact.Imports...)
		merged.TestImports = append(merged.TestImports, fact.TestImports...)
	}
	return merged, nil
}

func (*rustLang) GenerateRules(args language.GenerateArgs) language.GenerateResult {
	var result language.GenerateResult
	cfg := getConfig(args.Config)
	if !cfg.enabled || (cfg.owned && cfg.owner != args.Rel) {
		return result
	}
	plans, err := discover(args)
	if err != nil {
		diagnostic(args.Config, "gazelle_rs: %s: %v", args.Rel, err)
		return result
	}
	// A dedicated test file may be a child module, not an independent crate.
	// Reuse production extraction for generation so each graph is parsed once.
	type sourceGraph struct {
		fact *pb.CrateResult
		err  error
	}
	graphs := map[string]sourceGraph{}
	owned := map[string]bool{}
	ownershipComplete := true
	for _, p := range plans {
		if p.kind == "rust_test" && p.existing == nil {
			continue
		}
		root := filepath.Join(args.Dir, p.root)
		if _, ok := graphs[root]; ok {
			continue
		}
		fact, err := extractPlan(args.Dir, p)
		graph := sourceGraph{err: err, fact: fact}
		if err == nil {
			for _, source := range graph.fact.Sources {
				owned[filepath.Clean(source)] = true
			}
		} else {
			ownershipComplete = false
		}
		graphs[root] = graph
	}
	var active []plan
	var automaticRoots []string
	seenNames := map[string]bool{}
	for _, p := range plans {
		if p.autoTest && (!ownershipComplete || owned[filepath.Join(args.Dir, p.root)]) {
			continue
		}
		if p.autoTest {
			automaticRoots = append(automaticRoots, p.root)
			continue
		}
		if seenNames[p.name] {
			diagnostic(args.Config, "gazelle_rs: %s: duplicate generated Rust target %s", args.Rel, p.name)
			return result
		}
		seenNames[p.name] = true
		active = append(active, p)
	}
	if len(automaticRoots) > 0 {
		sort.Strings(automaticRoots)
		aggregate := -1
		for i, p := range active {
			if p.kind == "rust_test" && len(p.roots) > 0 {
				if aggregate >= 0 {
					aggregate = -2
					break
				}
				aggregate = i
			}
		}
		if aggregate >= 0 {
			p := &active[aggregate]
			p.roots = append(p.roots, automaticRoots...)
			delete(graphs, filepath.Join(args.Dir, p.root))
		} else if aggregate == -2 {
			diagnostic(args.Config, "gazelle_rs: %s: multiple aggregate tests; assign new test sources explicitly", args.Rel)
		} else {
			name := crateName(filepath.Base(args.Dir))
			if cfg.crateName != "" {
				name = cfg.crateName
			}
			name += "_test"
			if seenNames[name] {
				diagnostic(args.Config, "gazelle_rs: %s: aggregate test target %q is already owned; add the test sources to that target", args.Rel, name)
			} else {
				active = append(active, plan{name: name, root: "@aggregate:" + name, roots: automaticRoots, kind: "rust_test", edition: cfg.edition})
				seenNames[name] = true
			}
		}
	}
	existingNames := map[string]bool{}
	if args.File != nil {
		for _, r := range args.File.Rules {
			existingNames[r.Name()] = true
		}
	}
	// Explicit test roots own their variants. Legacy crate-based tests need the
	// library's full source graph, so leave that library untouched until migrated.
	testedRoots, legacyOwners := map[string]bool{}, map[string]bool{}
	for _, p := range active {
		if p.kind == "rust_test" {
			testedRoots[p.root] = true
		}
	}
	if args.File != nil {
		for _, r := range args.File.Rules {
			if baseKind(args.Config, r.Kind()) != "rust_test" {
				continue
			}
			owner := r.AttrString("crate")
			owner = strings.TrimPrefix(owner, "//"+args.Rel+":")
			owner = strings.TrimPrefix(owner, ":")
			if owner != "" && !strings.ContainsAny(owner, "/:@") {
				legacyOwners[owner] = true
			}
		}
	}
	for _, p := range active {
		graph := graphs[filepath.Join(args.Dir, p.root)]
		if p.kind != "rust_library" && p.kind != "rust_binary" {
			continue
		}
		if graph.fact == nil || len(graph.fact.TestSources) == 0 || testedRoots[p.root] || legacyOwners[p.name] {
			continue
		}
		if p.existing != nil && !literalList(p.existing, "srcs") {
			continue
		}
		if p.existing != nil && (!literalList(p.existing, "deps") || !literalList(p.existing, "proc_macro_deps")) {
			diagnostic(args.Config, "gazelle_rs: %s: %s has computed dependencies; declare a rust_test with crate_root = %q and its dependency expressions", args.Rel, p.name, p.root)
			continue
		}
		name := p.name + "_test"
		if existingNames[name] || seenNames[name] {
			diagnostic(args.Config, "gazelle_rs: %s: unit test name %q is already owned; declare a rust_test with crate_root = %q", args.Rel, name, p.root)
			continue
		}
		owner := p.existing
		if owner == nil {
			owner = rule.NewRule(p.kind, p.name)
		}
		active = append(active, plan{name: name, root: p.root, kind: "rust_test", edition: p.edition, owner: owner})
		testedRoots[p.root], seenNames[name] = true, true
	}
	for _, p := range active {
		root := filepath.Join(args.Dir, p.root)
		if p.existing == nil && existingNames[p.name] {
			diagnostic(args.Config, "gazelle_rs: %s: target name %q is already owned", args.Rel, p.name)
			continue
		}
		graph, ok := graphs[root]
		if !ok {
			graph.fact, graph.err = extractPlan(args.Dir, p)
			graphs[root] = graph
		}
		if graph.err != nil {
			diagnostic(args.Config, "gazelle_rs: %s: %v (leaving target unchanged)", args.Rel, graph.err)
			continue
		}
		if p.existing != nil && !literalList(p.existing, "srcs") {
			continue
		}
		fact := graph.fact
		if legacyOwners[p.name] && len(fact.TestSources) > 0 {
			continue
		}
		var srcs []string
		valid := true
		testSources := map[string]bool{}
		for _, src := range fact.TestSources {
			testSources[src] = true
		}
		seenSources := map[string]bool{}
		for _, src := range fact.Sources {
			if seenSources[src] {
				continue
			}
			seenSources[src] = true
			if p.kind != "rust_test" && testSources[src] {
				continue
			}
			rel, err := filepath.Rel(args.Dir, src)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				diagnostic(args.Config, "gazelle_rs: source outside package: %s", src)
				valid = false
				break
			}
			// A source label must not cross a nested Bazel package.
			for d := filepath.Dir(rel); d != "."; d = filepath.Dir(d) {
				for _, buildName := range args.Config.ValidBuildFileNames {
					if exists(filepath.Join(args.Dir, d, buildName)) {
						diagnostic(args.Config, "gazelle_rs: source %s crosses Bazel package %s", src, d)
						valid = false
					}
				}
			}
			srcs = append(srcs, filepath.ToSlash(rel))
		}
		if !valid {
			continue
		}
		sort.Strings(srcs)
		r := rule.NewRule(p.kind, p.name)
		r.SetAttr("srcs", srcs)
		seedResolveAttrs(r, p.existing)
		if p.owner != nil {
			seedResolveAttrs(r, p.owner)
			for _, attr := range []string{"crate_features", "compile_data", "rustc_env", "rustc_env_files", "rustc_flags"} {
				if value := p.owner.Attr(attr); value != nil {
					r.SetAttr(attr, value)
				}
			}
		}
		if len(p.roots) == 0 {
			r.SetAttr("crate_root", filepath.ToSlash(p.root))
		}
		r.SetAttr("edition", p.edition)
		cn := crateName(p.name)
		if p.owner != nil {
			cn = crateName(p.owner.Name())
			if n := p.owner.AttrString("crate_name"); n != "" {
				cn = n
			}
		}
		if p.existing != nil && p.existing.AttrString("crate_name") != "" {
			cn = p.existing.AttrString("crate_name")
		}
		r.SetAttr("crate_name", cn)
		if p.existing == nil && len(cfg.visibility) > 0 {
			r.SetAttr("visibility", cfg.visibility)
		}
		names := append([]string{}, fact.Imports...)
		if p.kind == "rust_test" {
			names = append(names, fact.TestImports...)
		}
		preserve := p.existing != nil && (!literalList(p.existing, "deps") || !literalList(p.existing, "proc_macro_deps"))
		result.Gen = append(result.Gen, r)
		result.Imports = append(result.Imports, importData{names: names, preserve: preserve})
	}
	return result
}
