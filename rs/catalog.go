package rs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/bazelbuild/bazel-gazelle/label"
)

var rustIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func loadCrateCatalog(file string) (*cargoIndex, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Version int `json:"version"`
		Crates  []struct {
			Name      string   `json:"name"`
			Label     string   `json:"label"`
			ProcMacro bool     `json:"proc_macro"`
			Aliases   []string `json:"aliases"`
		} `json:"crates"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(interface{})); err != io.EOF {
		return nil, fmt.Errorf("unexpected content after crate catalog")
	}
	if doc.Version != 1 || doc.Crates == nil {
		return nil, fmt.Errorf("crate catalog requires version 1 and a crates array")
	}
	index := &cargoIndex{crates: map[string][]externalCrate{}, packages: map[string]map[string][]externalCrate{}, byLabel: map[string]externalCrate{}}
	for _, entry := range doc.Crates {
		if !rustIdentifier.MatchString(entry.Name) {
			return nil, fmt.Errorf("invalid crate name %q", entry.Name)
		}
		parsed, err := catalogLabel(entry.Label)
		if err != nil {
			return nil, err
		}
		c := externalCrate{label: parsed, name: entry.Name, macro: entry.ProcMacro}
		for _, value := range append([]string{entry.Label}, entry.Aliases...) {
			alias, err := catalogLabel(value)
			if err != nil {
				return nil, err
			}
			key := alias.String()
			if previous, ok := index.byLabel[key]; ok && previous != c {
				return nil, fmt.Errorf("conflicting catalog label %s", key)
			}
			index.byLabel[key] = c
		}
		index.crates[c.name] = appendCrate(index.crates[c.name], c)
	}
	return index, nil
}
func catalogLabel(value string) (label.Label, error) {
	l, err := label.Parse(value)
	if err != nil || l.Relative || strings.HasPrefix(value, "@@") || !strings.Contains(value, "//") {
		return label.Label{}, fmt.Errorf("invalid catalog label %q; use an absolute apparent label", value)
	}
	return l, nil
}
