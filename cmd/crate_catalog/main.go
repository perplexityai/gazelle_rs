// crate_catalog exports Rust library aliases from bazel query --output=xml.
package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

type attribute struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}
type queryRule struct {
	Name    string      `xml:"name,attr"`
	Kind    string      `xml:"class,attr"`
	Strings []attribute `xml:"string"`
	Labels  []attribute `xml:"label"`
}

func attr(attrs []attribute, name string) string {
	for _, a := range attrs {
		if a.Name == name {
			return a.Value
		}
	}
	return ""
}

type crate struct {
	Name      string   `json:"name"`
	Label     string   `json:"label"`
	ProcMacro bool     `json:"proc_macro,omitempty"`
	Aliases   []string `json:"aliases,omitempty"`
}
type catalog struct {
	Version int     `json:"version"`
	Crates  []crate `json:"crates"`
}

func export(data []byte, prefix string) (catalog, error) {
	result := catalog{Version: 1, Crates: []crate{}}
	if !strings.HasSuffix(prefix, ":") || !strings.Contains(prefix, "//") {
		return result, fmt.Errorf("prefix must be a Bazel package label ending in colon")
	}
	// Bazel emits an XML 1.1 declaration; query data uses XML 1.0 syntax.
	data = bytes.Replace(data, []byte(`<?xml version="1.1"`), []byte(`<?xml version="1.0"`), 1)
	var query struct {
		XMLName xml.Name    `xml:"query"`
		Rules   []queryRule `xml:"rule"`
	}
	if err := xml.Unmarshal(data, &query); err != nil {
		return result, err
	}
	rules := map[string]queryRule{}
	for _, r := range query.Rules {
		rules[r.Name] = r
	}
	groups := map[string][]string{}
	for _, r := range query.Rules {
		if !strings.HasPrefix(r.Name, prefix) {
			continue
		}
		target := r
		seen := map[string]bool{}
		for target.Kind == "alias" {
			if seen[target.Name] {
				return result, fmt.Errorf("alias cycle at %s", target.Name)
			}
			seen[target.Name] = true
			actual := attr(target.Labels, "actual")
			var ok bool
			target, ok = rules[actual]
			if !ok {
				return result, fmt.Errorf("missing alias target %s; increase query deps depth", actual)
			}
		}
		if target.Kind != "rust_library" && target.Kind != "rust_proc_macro" {
			continue
		}
		groups[target.Name] = append(groups[target.Name], r.Name)
	}
	for target, labels := range groups {
		sort.Strings(labels)
		// Prefer a versioned alias over its unversioned prefix when both exist.
		preferred := labels[0]
		for _, candidate := range labels[1:] {
			if strings.HasPrefix(candidate, preferred+"-") {
				preferred = candidate
			}
		}
		var aliases []string
		for _, candidate := range labels {
			if candidate != preferred {
				aliases = append(aliases, candidate)
			}
		}
		r := rules[target]
		name := attr(r.Strings, "crate_name")
		if name == "" {
			name = strings.ReplaceAll(attr(r.Strings, "name"), "-", "_")
		}
		if name == "" {
			return result, fmt.Errorf("no crate name for %s", target)
		}
		result.Crates = append(result.Crates, crate{Name: name, Label: preferred, ProcMacro: r.Kind == "rust_proc_macro", Aliases: aliases})
	}
	if len(result.Crates) == 0 {
		return result, fmt.Errorf("no Rust crates under %s", prefix)
	}
	sort.Slice(result.Crates, func(i, j int) bool { return result.Crates[i].Label < result.Crates[j].Label })
	return result, nil
}
func run() error {
	input := flag.String("input", "", "Bazel query XML file (default stdin)")
	prefix := flag.String("prefix", "@crates//:", "Package label prefix to export")
	flag.Parse()
	var data []byte
	var err error
	if *input == "" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(*input)
	}
	if err != nil {
		return err
	}
	result, err := export(data, *prefix)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
