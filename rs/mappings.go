package rs

import "github.com/bazelbuild/bazel-gazelle/config"

// Gazelle applies KindMap even when GenerateRules returns no rules. Retain the
// inherited Rust mappings separately so disabled packages can still opt in below.
func scopeKindMappings(c *config.Config, cfg *rustConfig) {
	mappings := make(map[string]config.MappedKind)
	for k, v := range cfg.kindMap {
		mappings[k] = v
	}
	for k, v := range c.KindMap {
		mappings[k] = v
	}
	aliases := make(map[string]string)
	for k, v := range c.AliasMap {
		aliases[k] = v
	}
	cfg.kindMap = make(map[string]config.MappedKind)
	for _, kind := range []string{"rust_library", "rust_binary", "rust_test", "rust_proc_macro"} {
		seen := make(map[string]bool)
		for current := kind; !seen[current]; {
			seen[current] = true
			mapping, ok := mappings[current]
			if !ok {
				break
			}
			cfg.kindMap[current] = mapping
			// Aliases keep handwritten wrappers indexable without rewriting them.
			if mapping.KindName != kind {
				if _, exists := aliases[mapping.KindName]; !exists {
					aliases[mapping.KindName] = kind
				}
			}
			current = mapping.KindName
		}
	}
	if !cfg.enabled {
		for kind := range cfg.kindMap {
			delete(mappings, kind)
		}
	}
	c.KindMap = mappings
	c.AliasMap = aliases
}
