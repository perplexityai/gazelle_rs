package rs

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/bazelbuild/bazel-gazelle/label"
)

type cargoLockDependency struct {
	Package   string
	Version   string
	Path      string
	Git       string
	Registry  string
	Tag       string
	Branch    string
	Rev       string
	Workspace bool
}

type cargoLockedPackage struct{ Name, Version, Source string }

// Central manifests select direct dependencies; the lockfile supplies resolved
// versions. Neither describes library target names or procedural macro kinds.
func loadCargoLock(repository, manifestFile, lockFile, exceptionsFile string) (*cargoIndex, error) {
	if !repositoryName.MatchString(repository) {
		return nil, fmt.Errorf("invalid Cargo repository %q", repository)
	}
	var manifest struct {
		Dependencies map[string]toml.Primitive
		Workspace    struct{ Dependencies map[string]toml.Primitive }
		Patch        map[string]map[string]toml.Primitive
	}
	md, err := toml.DecodeFile(manifestFile, &manifest)
	if err != nil {
		return nil, fmt.Errorf("read central Cargo manifest: %w", err)
	}
	declarations := map[string]cargoLockDependency{}
	for _, deps := range []map[string]toml.Primitive{manifest.Workspace.Dependencies, manifest.Dependencies} {
		for name, raw := range deps {
			var version string
			var dep cargoLockDependency
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
	patches := map[string][]cargoLockDependency{}
	for name, raw := range manifest.Patch["crates-io"] {
		var patch cargoLockDependency
		if err := md.PrimitiveDecode(raw, &patch); err != nil {
			return nil, fmt.Errorf("patch %s: %w", name, err)
		}
		if patch.Package == "" {
			patch.Package = name
		}
		patches[patch.Package] = append(patches[patch.Package], patch)
	}
	var lock struct {
		Version int
		Package []cargoLockedPackage
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
	byPackage := map[string][]cargoLockedPackage{}
	nonRegistry := map[string]bool{}
	for _, pkg := range lock.Package {
		byPackage[pkg.Name] = append(byPackage[pkg.Name], pkg)
		if !strings.HasPrefix(pkg.Source, "registry+") && !strings.HasPrefix(pkg.Source, "sparse+") {
			nonRegistry[pkg.Name] = true
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
		// Git dependencies are selected separately from explicit manifest declarations.
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
		// A patch is a candidate source, not an unconditional replacement.
		// Only use it when the lockfile confirms that Cargo selected it.
		if dep.Git == "" && dep.Path == "" && dep.Registry == "" && dep.Version != "" {
			candidates, packages := patches[dep.Package], byPackage[dep.Package]
			if len(candidates) == 1 && len(packages) == 1 {
				patch := candidates[0]
				if patch.Git != "" && patch.Path == "" && matchesGitSource(patch, packages[0].Source) {
					dep.Git, dep.Tag, dep.Branch, dep.Rev = patch.Git, patch.Tag, patch.Branch, patch.Rev
					if patch.Version != "" {
						dep.Version += ", " + patch.Version
					}
				}
			}
		}
		if dep.Git != "" {
			// The unversioned hub alias is safe only with one locked identity.
			// Never fall back to a registry crate for an unmatched Git declaration.
			selected[name] = exceptions.candidates("", crateName(name))
			if len(selected[name]) > 0 {
				continue
			}
			packages := byPackage[dep.Package]
			if dep.Path != "" || len(packages) != 1 || !matchesGitSource(dep, packages[0].Source) {
				continue
			}
			pkg := packages[0]
			if pkg.Version == "" || !rustIdentifier.MatchString(crateName(pkg.Name)) {
				return nil, fmt.Errorf("invalid locked Git package %q", pkg.Name)
			}
			if dep.Version != "" {
				matches, err := cargoVersionMatches(dep.Version, pkg.Version)
				if err != nil {
					return nil, fmt.Errorf("dependency %s: %w", name, err)
				}
				if !matches {
					continue
				}
			}
			c := externalCrate{
				label: label.New(strings.TrimPrefix(repository, "@"), "", dep.Package),
				name:  crateName(dep.Package), inferred: true,
			}
			if exceptions != nil {
				if replacement, ok := exceptions.byLabel[c.label.String()]; ok {
					c = replacement
				}
			}
			selected[name] = []externalCrate{c}
			packageSelections[dep.Package] = appendCrate(packageSelections[dep.Package], c)
			index.byLabel[c.label.String()] = c
			continue
		}
		if dep.Path != "" || dep.Version == "" {
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
			if len(packageSelections[dep.Package]) == 1 && c.inferred && !nonRegistry[dep.Package] {
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

// Match the declared reference as well as the repository. The lockfile records
// the resolved commit in its fragment; no network lookup or URL guessing is needed.
func matchesGitSource(dep cargoLockDependency, source string) bool {
	if !strings.HasPrefix(source, "git+") {
		return false
	}
	declared, err := url.Parse(dep.Git)
	if err != nil || declared.RawQuery != "" || declared.Fragment != "" {
		return false
	}
	locked, err := url.Parse(strings.TrimPrefix(source, "git+"))
	if err != nil || locked.Fragment == "" {
		return false
	}
	query, err := url.ParseQuery(locked.RawQuery)
	if err != nil {
		return false
	}
	expected := url.Values{}
	for key, value := range map[string]string{"tag": dep.Tag, "branch": dep.Branch, "rev": dep.Rev} {
		if value != "" {
			expected.Set(key, value)
		}
	}
	if len(expected) > 1 || query.Encode() != expected.Encode() {
		return false
	}
	locked.RawQuery, locked.Fragment = "", ""
	locked.RawFragment = ""
	return declared.String() == locked.String()
}
