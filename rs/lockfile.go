package rs

import (
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/bazelbuild/bazel-gazelle/label"
)

// Central manifests describe import aliases; the lockfile supplies resolved
// versions. Neither describes library target names or procedural macro kinds.
func loadCargoLock(repository, manifestFile, lockFile, exceptionsFile string) (*cargoIndex, error) {
	if !repositoryName.MatchString(repository) {
		return nil, fmt.Errorf("invalid Cargo repository %q", repository)
	}
	type dependency struct {
		Package string
		Path    string
		Git     string
	}
	var manifest struct {
		Dependencies map[string]toml.Primitive
		Workspace    struct{ Dependencies map[string]toml.Primitive }
	}
	md, err := toml.DecodeFile(manifestFile, &manifest)
	if err != nil {
		return nil, fmt.Errorf("read central Cargo manifest: %w", err)
	}
	aliases := map[string][]string{}
	for _, deps := range []map[string]toml.Primitive{manifest.Workspace.Dependencies, manifest.Dependencies} {
		for name, raw := range deps {
			var version string
			if md.PrimitiveDecode(raw, &version) == nil {
				continue
			}
			var dep dependency
			if err := md.PrimitiveDecode(raw, &dep); err != nil {
				return nil, fmt.Errorf("dependency %s: %w", name, err)
			}
			if dep.Package != "" && dep.Path == "" && dep.Git == "" {
				aliases[dep.Package] = append(aliases[dep.Package], crateName(name))
			}
		}
	}
	var lock struct {
		Version int
		Package []struct{ Name, Version, Source string }
	}
	if _, err := toml.DecodeFile(lockFile, &lock); err != nil {
		return nil, fmt.Errorf("read Cargo lockfile: %w", err)
	}
	if lock.Version < 1 || lock.Version > 4 {
		return nil, fmt.Errorf("unsupported Cargo.lock version %d", lock.Version)
	}
	index := &cargoIndex{crates: map[string][]externalCrate{}, packages: map[string]map[string][]externalCrate{}, byLabel: map[string]externalCrate{}}
	var exceptions *cargoIndex
	if exceptionsFile != "" {
		exceptions, err = loadCrateCatalog(exceptionsFile)
		if err != nil {
			return nil, err
		}
	}
	sources := map[string]string{}
	unversioned := map[string][]externalCrate{}
	for _, pkg := range lock.Package {
		// Workspace and git packages have no portable versioned hub-label convention.
		if !strings.HasPrefix(pkg.Source, "registry+") && !strings.HasPrefix(pkg.Source, "sparse+") {
			continue
		}
		if pkg.Name == "" || pkg.Version == "" || !rustIdentifier.MatchString(crateName(pkg.Name)) {
			return nil, fmt.Errorf("invalid locked package %q", pkg.Name)
		}
		l := label.New(strings.TrimPrefix(repository, "@"), "", pkg.Name+"-"+pkg.Version)
		key := l.String()
		if previous, ok := sources[key]; ok && previous != pkg.Source {
			return nil, fmt.Errorf("locked packages from different registries collide at %s", key)
		}
		sources[key] = pkg.Source
		c := externalCrate{label: l, name: crateName(pkg.Name), inferred: true}
		if exceptions != nil {
			if replacement, ok := exceptions.byLabel[key]; ok {
				c = replacement
			}
		}
		index.byLabel[key] = c
		index.byLabel[c.label.String()] = c
		index.crates[c.name] = appendCrate(index.crates[c.name], c)
		for _, alias := range aliases[pkg.Name] {
			index.crates[alias] = appendCrate(index.crates[alias], c)
		}
		alias := label.New(strings.TrimPrefix(repository, "@"), "", pkg.Name).String()
		unversioned[alias] = appendCrate(unversioned[alias], c)
	}
	for alias, candidates := range unversioned {
		if len(candidates) == 1 {
			index.byLabel[alias] = candidates[0]
		}
	}
	if exceptions != nil {
		for name, crates := range exceptions.crates {
			for _, c := range crates {
				index.crates[name] = appendCrate(index.crates[name], c)
			}
		}
		for key, c := range exceptions.byLabel {
			index.byLabel[key] = c
		}
	}
	return index, nil
}
