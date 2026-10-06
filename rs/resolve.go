package rs

import (
	"log"
	"sort"

	"github.com/bazelbuild/bazel-gazelle/config"
	"github.com/bazelbuild/bazel-gazelle/label"
	"github.com/bazelbuild/bazel-gazelle/repo"
	"github.com/bazelbuild/bazel-gazelle/resolve"
	"github.com/bazelbuild/bazel-gazelle/rule"
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
	deps, macros := map[string]bool{}, map[string]bool{}
	unresolved := false
	for _, name := range data.names {
		spec := resolve.ImportSpec{Lang: languageName, Imp: name}
		dep, overridden := resolve.FindRuleWithOverride(c, spec, languageName)
		if !overridden {
			hits := ix.FindRulesByImportWithConfig(c, spec, languageName)
			if len(hits) == 0 {
				unresolved = true
				log.Printf("gazelle_rs: %s: unresolved crate %q; add # gazelle:resolve rs %s <label>", from.String(), name, name)
				continue
			}
			if len(hits) > 1 {
				unresolved = true
				log.Printf("gazelle_rs: %s: ambiguous crate %q; add # gazelle:resolve rs %s <label>", from.String(), name, name)
				continue
			}
			dep = hits[0].Label
		}
		if dep.Equal(from) {
			continue
		}
		value := dep.Rel(from.Repo, from.Pkg).String()
		isMacro := false
		for _, hit := range ix.FindRulesByImportWithConfig(c, resolve.ImportSpec{Lang: languageName, Imp: "proc-macro:" + name}, languageName) {
			if hit.Label.Equal(dep) {
				isMacro = true
			}
		}
		if isMacro {
			macros[value] = true
		} else {
			deps[value] = true
		}
	}
	// Keep existing dependency expressions intact if extraction cannot resolve every import.
	if unresolved {
		return
	}
	for attr, values := range map[string]map[string]bool{"deps": deps, "proc_macro_deps": macros} {
		var list []string
		for value := range values {
			list = append(list, value)
		}
		sort.Strings(list)
		if len(list) > 0 {
			r.SetAttr(attr, list)
		} else {
			r.DelAttr(attr)
		}
	}
}
