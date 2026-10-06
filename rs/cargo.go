package rs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bazelbuild/bazel-gazelle/label"
)

// cargo metadata --format-version=1 exposes the library name and target kind;
// neither can be inferred reliably from a Cargo.lock package name.
type cargoMetadata struct {
	Version          int      `json:"version"`
	WorkspaceRoot    string   `json:"workspace_root"`
	WorkspaceMembers []string `json:"workspace_members"`
	Packages         []struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		Version      string `json:"version"`
		ManifestPath string `json:"manifest_path"`
		Targets      []struct {
			Name string   `json:"name"`
			Kind []string `json:"kind"`
		} `json:"targets"`
	} `json:"packages"`
	Resolve *struct {
		Nodes []struct {
			ID   string `json:"id"`
			Deps []struct {
				Name     string `json:"name"`
				Pkg      string `json:"pkg"`
				DepKinds []struct {
					Kind string `json:"kind"`
				} `json:"dep_kinds"`
			} `json:"deps"`
		} `json:"nodes"`
	} `json:"resolve"`
}

type externalCrate struct {
	label label.Label
	name  string
	macro bool
}

type cargoIndex struct {
	crates   map[string][]externalCrate
	packages map[string]map[string][]externalCrate
	byLabel  map[string]externalCrate
}

var repositoryName = regexp.MustCompile(`^@[A-Za-z0-9._+-]+$`)

func loadCargoIndex(repository, file string) (*cargoIndex, error) {
	if !repositoryName.MatchString(repository) {
		return nil, fmt.Errorf("invalid Cargo repository %q; expected @repository", repository)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read Cargo metadata: %w", err)
	}
	var metadata cargoMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("decode Cargo metadata: %w", err)
	}
	if metadata.Version != 1 || metadata.Resolve == nil {
		return nil, fmt.Errorf("Cargo metadata must use --format-version=1 without --no-deps")
	}
	index := &cargoIndex{crates: make(map[string][]externalCrate), packages: make(map[string]map[string][]externalCrate), byLabel: make(map[string]externalCrate)}
	members := make(map[string]bool)
	for _, id := range metadata.WorkspaceMembers {
		members[id] = true
	}
	byID := make(map[string]externalCrate)
	packagePaths := make(map[string]string)
	labelIDs := make(map[string]string)
	unversioned := make(map[string][]externalCrate)
	for _, pkg := range metadata.Packages {
		if members[pkg.ID] {
			rel, err := filepath.Rel(metadata.WorkspaceRoot, filepath.Dir(pkg.ManifestPath))
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				if rel == "." {
					rel = ""
				}
				packagePaths[pkg.ID] = filepath.ToSlash(rel)
			}
			continue
		}
		for _, target := range pkg.Targets {
			library, macro := false, false
			for _, kind := range target.Kind {
				switch kind {
				case "lib", "rlib", "dylib":
					library = true
				case "proc-macro":
					library, macro = true, true
				}
			}
			if !library {
				continue
			}
			if pkg.ID == "" || pkg.Name == "" || pkg.Version == "" || target.Name == "" {
				return nil, fmt.Errorf("incomplete library in Cargo metadata")
			}
			crate := externalCrate{label: label.New(strings.TrimPrefix(repository, "@"), "", pkg.Name+"-"+pkg.Version), name: crateName(target.Name), macro: macro}
			key := crate.label.String()
			if previous, ok := labelIDs[key]; ok && previous != pkg.ID {
				return nil, fmt.Errorf("Cargo packages %q and %q map to the same label %s", previous, pkg.ID, key)
			}
			labelIDs[key] = pkg.ID
			byID[pkg.ID] = crate
			index.crates[crate.name] = appendCrate(index.crates[crate.name], crate)
			index.byLabel[key] = crate
			alias := label.New(strings.TrimPrefix(repository, "@"), "", pkg.Name).String()
			unversioned[alias] = appendCrate(unversioned[alias], crate)
		}
	}
	// Explicit overrides may use a hub's unversioned alias. Only classify aliases
	// whose package version is unambiguous; automatic resolution stays versioned.
	for alias, crates := range unversioned {
		if len(crates) == 1 {
			index.byLabel[alias] = crates[0]
		}
	}
	for _, node := range metadata.Resolve.Nodes {
		pkg, ok := packagePaths[node.ID]
		if !ok {
			continue
		}
		deps := make(map[string][]externalCrate)
		for _, dep := range node.Deps {
			crate, ok := byID[dep.Pkg]
			if !ok {
				continue
			}
			normal := len(dep.DepKinds) == 0
			for _, kind := range dep.DepKinds {
				normal = normal || kind.Kind != "build"
			}
			if normal {
				deps[dep.Name] = appendCrate(deps[dep.Name], crate)
				index.crates[dep.Name] = appendCrate(index.crates[dep.Name], crate)
			}
		}
		index.packages[pkg] = deps
	}
	return index, nil
}

func appendCrate(crates []externalCrate, crate externalCrate) []externalCrate {
	for _, existing := range crates {
		if existing == crate {
			return crates
		}
	}
	return append(crates, crate)
}

func (index *cargoIndex) candidates(pkg, name string) []externalCrate {
	if index == nil {
		return nil
	}
	if deps := index.packages[pkg][name]; len(deps) > 0 {
		return deps
	}
	return index.crates[name]
}
