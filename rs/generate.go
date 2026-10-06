package rs

import (
	"fmt"
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
	existing                  *rule.Rule
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

// Seed existing resolution attributes so an incomplete extraction or a computed
// dependency expression cannot turn an early Resolve return into a deletion.
func seedResolveAttrs(dst, src *rule.Rule) {
	if src == nil {
		return
	}
	for _, attr := range []string{"deps", "proc_macro_deps"} {
		if value := src.Attr(attr); value != nil {
			dst.SetAttr(attr, value)
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
	if exists(cargo) {
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
	add := func(t target, kind, fallback, defaultRoot string) {
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
			plans = append(plans, plan{name: crateName(t.Name), root: t.Path, kind: kind, edition: edition})
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
	add(doc.Lib, "rust_library", name, choose("src/lib.rs", "lib.rs"))
	binaryName := name
	if len(plans) > 0 {
		binaryName += "_bin"
	}
	if len(doc.Bin) > 0 {
		for _, t := range doc.Bin {
			add(t, "rust_binary", binaryName, "src/main.rs")
		}
	} else {
		add(target{}, "rust_binary", binaryName, choose("src/main.rs", "main.rs"))
	}
	for _, pattern := range []string{"src/bin/*.rs", "src/bin/*/main.rs", "tests/*.rs", "*.test.rs", "src/*.test.rs"} {
		files, err := filepath.Glob(filepath.Join(args.Dir, pattern))
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			root, _ := filepath.Rel(args.Dir, file)
			n := strings.TrimSuffix(filepath.Base(file), ".rs")
			if n == "main" {
				n = filepath.Base(filepath.Dir(file))
			}
			kind := "rust_binary"
			if strings.HasPrefix(pattern, "tests/") || strings.HasSuffix(file, ".test.rs") {
				kind = "rust_test"
				n = strings.TrimSuffix(n, ".test") + "_test"
			}
			add(target{Name: n, Path: root}, kind, n, root)
		}
	}
	for _, t := range doc.Test {
		add(t, "rust_test", t.Name+"_test", "")
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
			if root != "" && literalList(r, "srcs") {
				existingEdition := edition
				if e := r.AttrString("edition"); e != "" {
					existingEdition = e
				}
				plans = append(plans, plan{name: r.Name(), root: root, kind: kind, edition: existingEdition, existing: r})
			}
		}
	}
	seenNames, seenRoots := map[string]bool{}, map[string]bool{}
	var out []plan
	for _, p := range plans {
		if excludedRoot(cfg, args.Rel, p.root) {
			continue
		}
		if p.existing == nil && seenRoots[p.kind+":"+p.root] {
			continue
		}
		if seenNames[p.name] {
			return nil, fmt.Errorf("duplicate generated Rust target %s", p.name)
		}
		seenNames[p.name] = true
		seenRoots[p.kind+":"+p.root] = true
		out = append(out, p)
	}
	return out, nil
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
	existingNames := map[string]bool{}
	if args.File != nil {
		for _, r := range args.File.Rules {
			existingNames[r.Name()] = true
		}
	}
	for _, p := range plans {
		if p.existing == nil && existingNames[p.name] {
			diagnostic(args.Config, "gazelle_rs: %s: target name %q is already owned", args.Rel, p.name)
			continue
		}
		facts, err := extract([]string{filepath.Join(args.Dir, p.root)})
		if err != nil {
			diagnostic(args.Config, "gazelle_rs: %s: %v (leaving target unchanged)", args.Rel, err)
			continue
		}
		fact := facts[0]
		var srcs []string
		valid := true
		for _, src := range fact.Sources {
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
		r.SetAttr("crate_root", filepath.ToSlash(p.root))
		r.SetAttr("edition", p.edition)
		cn := crateName(p.name)
		if p.existing != nil && p.existing.AttrString("crate_name") != "" {
			cn = p.existing.AttrString("crate_name")
		}
		r.SetAttr("crate_name", cn)
		if p.existing == nil {
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
