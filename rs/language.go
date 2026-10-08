// Package rs implements a Gazelle language for Rust crates.
package rs

import (
	"flag"
	"maps"
	"path"
	"path/filepath"
	"strings"

	"github.com/bazelbuild/bazel-gazelle/config"
	"github.com/bazelbuild/bazel-gazelle/label"
	"github.com/bazelbuild/bazel-gazelle/language"
	"github.com/bazelbuild/bazel-gazelle/rule"
)

const languageName = "rs"

type rustLang struct{}

func NewLanguage() language.Language                                  { return &rustLang{} }
func (*rustLang) Name() string                                        { return languageName }
func (*rustLang) RegisterFlags(*flag.FlagSet, string, *config.Config) {}
func (*rustLang) CheckFlags(*flag.FlagSet, *config.Config) error      { return nil }
func (*rustLang) Fix(*config.Config, *rule.File)                      {}
func (*rustLang) Embeds(*rule.Rule, label.Label) []label.Label        { return nil }
func (*rustLang) Kinds() map[string]rule.KindInfo {
	out := map[string]rule.KindInfo{}
	for _, kind := range []string{"rust_library", "rust_binary", "rust_test", "rust_proc_macro"} {
		out[kind] = rule.KindInfo{NonEmptyAttrs: map[string]bool{"name": true}, MergeableAttrs: map[string]bool{"srcs": true}, ResolveAttrs: map[string]bool{"deps": true, "proc_macro_deps": true, "aliases": true}}
	}
	return out
}
func (*rustLang) Loads() []rule.LoadInfo {
	var out []rule.LoadInfo
	for _, kind := range []string{"rust_library", "rust_binary", "rust_test", "rust_proc_macro"} {
		out = append(out, rule.LoadInfo{Name: "@rules_rs//rs:" + kind + ".bzl", Symbols: []string{kind}})
	}
	return out
}

type rustConfig struct {
	enabled            bool
	edition, crateName string
	visibility         []string
	excludes           []string
	owner              string
	owned              bool
	kindMap            map[string]config.MappedKind
	cargo              *cargoIndex
	procMacros         map[string]bool
}

func getConfig(c *config.Config) *rustConfig {
	if v, ok := c.Exts[languageName].(*rustConfig); ok {
		return v
	}
	return &rustConfig{enabled: true, edition: "2021"}
}
func (*rustLang) KnownDirectives() []string {
	return []string{"rust_extension", "rust_edition", "rust_crate_name", "rust_visibility", "rust_cargo_metadata", "rust_crate_catalog", "rust_cargo_lock", "rust_proc_macro"}
}
func (*rustLang) Configure(c *config.Config, rel string, f *rule.File) {
	cfg := *getConfig(c)
	cfg.procMacros = maps.Clone(cfg.procMacros)
	if cfg.procMacros == nil {
		cfg.procMacros = map[string]bool{}
	}
	cfg.excludes = append([]string(nil), cfg.excludes...)
	// Crate names apply only to this package; edition and visibility inherit.
	cfg.crateName = ""
	if f != nil {
		for _, d := range f.Directives {
			switch d.Key {
			case "exclude":
				cfg.excludes = append(cfg.excludes, path.Join(rel, d.Value))
			case "rust_extension":
				cfg.enabled = d.Value != "disabled" && d.Value != "false"
			case "rust_edition":
				switch d.Value {
				case "2015", "2018", "2021", "2024":
					cfg.edition = d.Value
				default:
					diagnostic(c, "%s: invalid Rust edition %q", rel, d.Value)
				}
			case "rust_crate_name":
				cfg.crateName = d.Value
			case "rust_visibility":
				cfg.visibility = strings.Fields(d.Value)
			case "rust_proc_macro":
				for _, name := range strings.Fields(d.Value) {
					if !rustIdentifier.MatchString(name) {
						diagnostic(c, "gazelle_rs: %s: invalid proc-macro crate name %q", rel, name)
						continue
					}
					cfg.procMacros[name] = true
				}
			case "rust_cargo_lock":
				args := strings.Fields(d.Value)
				cfg.cargo = nil
				if len(args) < 3 || len(args) > 4 {
					diagnostic(c, "gazelle_rs: %s: expected rust_cargo_lock @repository Cargo.toml Cargo.lock [exceptions.json]", rel)
					continue
				}
				exceptions := ""
				if len(args) == 4 {
					exceptions = filepath.Join(c.RepoRoot, args[3])
				}
				index, err := loadCargoLock(args[0], filepath.Join(c.RepoRoot, args[1]), filepath.Join(c.RepoRoot, args[2]), exceptions)
				if err != nil {
					diagnostic(c, "gazelle_rs: %s: %v", rel, err)
					continue
				}
				cfg.cargo = index
			case "rust_crate_catalog":
				cfg.cargo = nil
				index, err := loadCrateCatalog(filepath.Join(c.RepoRoot, d.Value))
				if err != nil {
					diagnostic(c, "gazelle_rs: %s: %v", rel, err)
					continue
				}
				cfg.cargo = index
			case "rust_cargo_metadata":
				args := strings.Fields(d.Value)
				cfg.cargo = nil
				if len(args) != 2 {
					diagnostic(c, "gazelle_rs: %s: expected rust_cargo_metadata @repository path/to/metadata.json", rel)
					continue
				}
				index, err := loadCargoIndex(args[0], filepath.Join(c.RepoRoot, args[1]))
				if err != nil {
					diagnostic(c, "gazelle_rs: %s: %v", rel, err)
					continue
				}
				cfg.cargo = index
			}
		}
	}
	scopeKindMappings(c, &cfg)
	// A nested BUILD can declare an independent crate even under an existing
	// crate's directory. Ordinary source subdirectories still inherit ownership.
	if cfg.owned && cfg.owner != rel && f != nil {
		for _, r := range f.Rules {
			switch baseKind(c, r.Kind()) {
			case "rust_library", "rust_binary", "rust_proc_macro":
				cfg.owned = false
			}
		}
	}
	if !cfg.owned {
		dir := filepath.Join(c.RepoRoot, filepath.FromSlash(rel))
		for _, root := range []string{"lib.rs", "main.rs", "src/lib.rs", "src/main.rs"} {
			if exists(filepath.Join(dir, root)) {
				cfg.owner = rel
				cfg.owned = true
				break
			}
		}
		// Bin-only packages and explicit nonstandard roots also own their source subtree.
		if !cfg.owned {
			if plans, err := discover(language.GenerateArgs{Config: c, Dir: dir, Rel: rel, File: f}); err == nil && len(plans) > 0 {
				cfg.owner, cfg.owned = rel, true
			}
		}
	}
	c.Exts[languageName] = &cfg
}
func crateName(name string) string { return strings.ReplaceAll(name, "-", "_") }
func baseKind(c *config.Config, kind string) string {
	if original, ok := c.AliasMap[kind]; ok {
		return original
	}
	for original, mapped := range c.KindMap {
		if mapped.KindName == kind {
			return original
		}
	}
	return kind
}
