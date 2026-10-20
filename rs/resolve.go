package rs

import (
	pb "github.com/perplexityai/gazelle_rs/rs/proto"
	"log"
	"sort"
	"strings"

	"github.com/bazelbuild/bazel-gazelle/config"
	"github.com/bazelbuild/bazel-gazelle/label"
	"github.com/bazelbuild/bazel-gazelle/repo"
	"github.com/bazelbuild/bazel-gazelle/resolve"
	"github.com/bazelbuild/bazel-gazelle/rule"
	"github.com/bazelbuild/buildtools/build"
)

func (*rustLang) Imports(c *config.Config, r *rule.Rule, f *rule.File) []resolve.ImportSpec {
	kind := baseKind(c, r.Kind())
	if kind != "rust_library" && kind != "rust_proc_macro" {
		return nil
	}
	name := r.AttrString("crate_name")
	if name == "" {
		name = crateName(r.Name())
	}
	specs := []resolve.ImportSpec{{Lang: languageName, Imp: name}}
	if kind == "rust_proc_macro" {
		specs = append(specs, resolve.ImportSpec{Lang: languageName, Imp: "proc-macro:" + name})
	}
	return specs
}
func (*rustLang) Resolve(c *config.Config, ix *resolve.RuleIndex, _ *repo.RemoteCache, r *rule.Rule, raw interface{}, from label.Label) {
	data, ok := raw.(importData)
	if !ok || data.preserve {
		return
	}
	deps, macros := map[string][]*pb.Cfg{}, map[string][]*pb.Cfg{}
	unresolved := false
	cargo := getConfig(c).cargo
	aliases, editableAliases := crateAliases(r)
	aliasesChanged := false
	importNames := make(map[string]string)
	for _, name := range data.names {
		spec := resolve.ImportSpec{Lang: languageName, Imp: name}
		dep, overridden := resolve.FindRuleWithOverride(c, spec, languageName)
		isMacro := false
		externalName := ""
		if !overridden && cargo != nil {
			var renamed []externalCrate
			for value, imported := range aliases {
				if imported == name {
					parsed, err := label.Parse(value)
					if err == nil {
						if candidate, ok := cargo.byLabel[parsed.Abs(from.Repo, from.Pkg).String()]; ok {
							renamed = appendCrate(renamed, candidate)
						}
					}
				}
			}
			if len(renamed) > 1 {
				unresolved = true
				diagnostic(c, "gazelle_rs: %s: ambiguous alias for crate %q", from.String(), name)
				continue
			}
			if len(renamed) == 1 {
				dep, isMacro, externalName = renamed[0].label, renamed[0].macro, renamed[0].name
				overridden = true
			}
		}
		if !overridden {
			hits := ix.FindRulesByImportWithConfig(c, spec, languageName)
			if len(hits) == 0 {
				candidates := cargo.candidates(from.Pkg, name)
				if len(candidates) > 1 {
					var selected []externalCrate
					for _, value := range dependencyLabels(r) {
						parsed, err := label.Parse(value)
						if err != nil {
							continue
						}
						known, ok := cargo.byLabel[parsed.Abs(from.Repo, from.Pkg).String()]
						if !ok {
							continue
						}
						for _, candidate := range candidates {
							if known == candidate {
								selected = appendCrate(selected, candidate)
							}
						}
					}
					if len(selected) == 1 {
						candidates = selected
					}
				}
				if len(candidates) != 1 {
					unresolved = true
					reason := "unresolved"
					if len(candidates) > 1 {
						reason = "ambiguous external"
					}
					diagnostic(c, "gazelle_rs: %s: %s crate %q; add # gazelle:resolve rs %s <label>", from.String(), reason, name, name)
					continue
				}
				dep, isMacro, externalName = candidates[0].label, candidates[0].macro, candidates[0].name
			}
			if len(hits) > 1 {
				selected := -1
				existing := dependencyLabels(r)
				if crate := r.AttrString("crate"); crate != "" {
					existing = append(existing, crate)
				}
				for i, hit := range hits {
					for _, value := range existing {
						old, err := label.Parse(value)
						if err == nil && old.Abs(from.Repo, from.Pkg).Equal(hit.Label) {
							if selected >= 0 && selected != i {
								selected = -2
								break
							}
							selected = i
							break
						}
					}
					if selected == -2 {
						break
					}
				}
				if selected >= 0 {
					hits = hits[selected : selected+1]
				}
			}
			if len(hits) > 1 {
				unresolved = true
				diagnostic(c, "gazelle_rs: %s: ambiguous crate %q; add # gazelle:resolve rs %s <label>", from.String(), name, name)
				continue
			}
			if len(hits) == 1 {
				dep = hits[0].Label
			}
		}
		if dep.Equal(from) {
			continue
		}
		value := dep.Rel(from.Repo, from.Pkg).String()
		if cargo != nil {
			if external, ok := cargo.byLabel[dep.String()]; ok {
				isMacro, externalName = external.macro, external.name
				// An explicit mapping supplies the import name when the lockfile
				// only guessed it from the package name.
				if overridden && external.inferred {
					externalName = ""
				}
			}
		}
		if externalName != "" {
			if previous, ok := importNames[value]; ok && previous != name {
				unresolved = true
				diagnostic(c, "gazelle_rs: %s: crate %s imported as both %s and %s; preserve explicit aliases", from.String(), value, previous, name)
				continue
			}
			importNames[value] = name
			if aliases[value] != "" && aliases[value] != name {
				unresolved = true
				diagnostic(c, "gazelle_rs: %s: alias for %s conflicts with import %s; preserve explicit aliases", from.String(), value, name)
				continue
			}
			if externalName != name {
				if !editableAliases {
					unresolved = true
					diagnostic(c, "gazelle_rs: %s: cannot add alias %s for %s; preserve explicit aliases", from.String(), name, value)
					continue
				}
				aliases[value] = name
				aliasesChanged = true
			}
		}
		for _, hit := range ix.FindRulesByImportWithConfig(c, resolve.ImportSpec{Lang: languageName, Imp: "proc-macro:" + name}, languageName) {
			if hit.Label.Equal(dep) {
				isMacro = true
			}
		}
		if isMacro || getConfig(c).procMacros[name] {
			macros[value] = append(macros[value], importConditions(data, name)...)
		} else {
			deps[value] = append(deps[value], importConditions(data, name)...)
		}
	}
	// Keep existing dependency expressions intact if extraction cannot resolve every import.
	if unresolved {
		return
	}
	keeps := map[string]map[string]bool{
		"deps":            keepCfgDeps(deps, r.Attr("deps"), getConfig(c).cfgLabels),
		"proc_macro_deps": keepCfgDeps(macros, r.Attr("proc_macro_deps"), getConfig(c).cfgLabels),
	}
	unsupported := map[string]bool{}
	expressions := map[string]build.Expr{}
	for attr, values := range map[string]map[string][]*pb.Cfg{"deps": deps, "proc_macro_deps": macros} {
		expr, err := conditionalDeps(values, getConfig(c).cfgLabels, data.testing, unsupported, keeps[attr])
		if err != nil {
			diagnostic(c, "gazelle_rs: %s: %v (leaving dependencies unchanged)", from.String(), err)
			return
		}
		expressions[attr] = expr
	}
	if len(unsupported) > 0 && (hasOSConditions(data.conditions) || unsupported["cfg_attr"]) {
		var predicates []string
		for name := range unsupported {
			predicates = append(predicates, name)
		}
		sort.Strings(predicates)
		log.Printf("gazelle_rs: %s: unsupported cfg predicates (%s); retaining dependencies conservatively within known OS guards", from.String(), strings.Join(predicates, ", "))
	}
	if aliasesChanged {
		r.SetAttr("aliases", aliasValue{rule.ExprFromValue(aliases)})
	}
	for attr, expr := range expressions {
		if expr == nil {
			r.DelAttr(attr)
		} else {
			r.SetAttr(attr, cfgDependencyValue{expr})
		}
	}
}

func crateAliases(r *rule.Rule) (map[string]string, bool) {
	aliases := make(map[string]string)
	if r.Attr("aliases") == nil {
		return aliases, true
	}
	dict, ok := r.Attr("aliases").(*build.DictExpr)
	if !ok {
		return aliases, false
	}
	for _, entry := range dict.List {
		key, keyOK := entry.Key.(*build.StringExpr)
		value, valueOK := entry.Value.(*build.StringExpr)
		if !keyOK || !valueOK {
			return aliases, false
		}
		aliases[key.Value] = value.Value
	}
	return aliases, true
}

// Gazelle's default dictionary merger handles select lists, not label-to-name
// dictionaries. Preserve handwritten aliases and their comments when adding keys.
type aliasValue struct{ expr build.Expr }

func (v aliasValue) BzlExpr() build.Expr { return v.expr }
func (v aliasValue) Merge(other build.Expr) build.Expr {
	if other == nil {
		return v.expr
	}
	src, srcOK := v.expr.(*build.DictExpr)
	dst, dstOK := other.(*build.DictExpr)
	if !srcOK || !dstOK {
		return other
	}
	merged := *dst
	merged.List = append([]*build.KeyValueExpr{}, dst.List...)
	keys := make(map[string]bool)
	for _, entry := range dst.List {
		key, ok := entry.Key.(*build.StringExpr)
		if !ok {
			return other
		}
		keys[key.Value] = true
	}
	for _, entry := range src.List {
		key, ok := entry.Key.(*build.StringExpr)
		if !ok {
			return other
		}
		if !keys[key.Value] {
			merged.List = append(merged.List, entry)
		}
	}
	return &merged
}

func importConditions(data importData, name string) []*pb.Cfg {
	if conditions := data.conditions[name]; len(conditions) > 0 {
		return conditions
	}
	return []*pb.Cfg{nil}
}
