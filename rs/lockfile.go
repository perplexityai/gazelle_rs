package rs

import (
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/bazelbuild/bazel-gazelle/label"
)

// Central manifests select direct dependencies; the lockfile supplies resolved
// versions. Neither describes library target names or procedural macro kinds.
func loadCargoLock(repository, manifestFile, lockFile, exceptionsFile string) (*cargoIndex, error) {
	if !repositoryName.MatchString(repository) {
		return nil, fmt.Errorf("invalid Cargo repository %q", repository)
	}
	type dependency struct {
		Package   string
		Version   string
		Path      string
		Git       string
		Workspace bool
	}
	var manifest struct {
		Dependencies map[string]toml.Primitive
		Workspace    struct{ Dependencies map[string]toml.Primitive }
	}
	md, err := toml.DecodeFile(manifestFile, &manifest)
	if err != nil {
		return nil, fmt.Errorf("read central Cargo manifest: %w", err)
	}
	declarations := map[string]dependency{}
	for _, deps := range []map[string]toml.Primitive{manifest.Workspace.Dependencies, manifest.Dependencies} {
		for name, raw := range deps {
			var version string
			var dep dependency
			if md.PrimitiveDecode(raw, &version) == nil {
				dep.Version = version
			} else if err := md.PrimitiveDecode(raw, &dep); err != nil {
				return nil, fmt.Errorf("dependency %s: %w", name, err)
			}
			if dep.Workspace {
				continue
			}
			if dep.Package == "" {
				dep.Package = name
			}
			declarations[name] = dep
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
	type lockedCrate struct {
		version string
		crate   externalCrate
	}
	locked := map[string][]lockedCrate{}
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
		locked[pkg.Name] = append(locked[pkg.Name], lockedCrate{pkg.Version, c})
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

	selected := map[string][]externalCrate{}
	packageSelections := map[string][]externalCrate{}
	for name, dep := range declarations {
		if dep.Path != "" || dep.Git != "" || dep.Version == "" {
			continue
		}
		if _, err := cargoVersionMatches(dep.Version, "0.0.0"); err != nil {
			return nil, fmt.Errorf("dependency %s: %w", name, err)
		}
		// A declaration with no locked match must not fall back to an incompatible version.
		selected[name] = nil
		for _, candidate := range locked[dep.Package] {
			matches, err := cargoVersionMatches(dep.Version, candidate.version)
			if err != nil {
				return nil, fmt.Errorf("dependency %s: %w", name, err)
			}
			if matches {
				selected[name] = appendCrate(selected[name], candidate.crate)
				packageSelections[dep.Package] = appendCrate(packageSelections[dep.Package], candidate.crate)
			}
		}
	}
	for name, candidates := range selected {
		dep := declarations[name]
		for i, c := range candidates {
			// Multiple direct versions cannot safely share one default hub alias.
			// Catalog labels are explicit and retain their original spelling.
			if len(packageSelections[dep.Package]) == 1 && c.inferred {
				c.label = label.New(strings.TrimPrefix(repository, "@"), "", dep.Package)
				index.byLabel[c.label.String()] = c
				candidates[i] = c
			}
		}
		importName := crateName(name)
		if name == dep.Package && len(candidates) == 1 && !candidates[0].inferred {
			importName = candidates[0].name
		}
		index.crates[importName] = candidates
	}
	return index, nil
}
