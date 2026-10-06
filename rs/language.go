// Package rs implements a Gazelle language for Rust crates.
package rs

import (
	"flag"
	"log"
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
	owner              string
	owned              bool
	kindMap            map[string]config.MappedKind
	cargo              *cargoIndex
}

func getConfig(c *config.Config) *rustConfig {
	if v, ok := c.Exts[languageName].(*rustConfig); ok {
		return v
	}
	return &rustConfig{enabled: true, edition: "2021", visibility: []string{"//visibility:public"}}
}
func (*rustLang) KnownDirectives() []string {
	return []string{"rust_extension", "rust_edition", "rust_crate_name", "rust_visibility", "rust_cargo_metadata"}
}
func (*rustLang) Configure(c *config.Config, rel string, f *rule.File) {
	cfg := *getConfig(c)
	// Crate names apply only to this package; edition and visibility inherit.
	cfg.crateName = ""
	if f != nil {
		for _, d := range f.Directives {
			switch d.Key {
			case "rust_extension":
				cfg.enabled = d.Value != "disabled" && d.Value != "false"
			case "rust_edition":
				switch d.Value {
				case "2015", "2018", "2021", "2024":
					cfg.edition = d.Value
				default:
					log.Printf("%s: invalid Rust edition %q", rel, d.Value)
				}
			case "rust_crate_name":
				cfg.crateName = d.Value
			case "rust_visibility":
				cfg.visibility = strings.Fields(d.Value)
			case "rust_cargo_metadata":
				args := strings.Fields(d.Value)
				cfg.cargo = nil
				if len(args) != 2 {
					log.Printf("gazelle_rs: %s: expected rust_cargo_metadata @repository path/to/metadata.json", rel)
					continue
				}
				index, err := loadCargoIndex(args[0], filepath.Join(c.RepoRoot, args[1]))
				if err != nil {
					log.Printf("gazelle_rs: %s: %v", rel, err)
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
