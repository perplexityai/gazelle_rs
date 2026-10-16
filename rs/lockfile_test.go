package rs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCentralCargoResolution(t *testing.T) {
	dir := t.TempDir()
	write := func(name, contents string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(contents), 0644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	manifest := write("Cargo.toml", `[workspace.dependencies]
renamed = { package = "wire-codec", version = "2" }
derive-package = "1"
`)
	lock := write("Cargo.lock", `version = 4
[[package]]
name = "wire-codec"
version = "1.0.0"
source = "registry+https://example.invalid/index"
[[package]]
name = "wire-codec"
version = "2.0.0"
source = "sparse+https://example.invalid/index"
[[package]]
name = "derive-package"
version = "1.0.0"
source = "registry+https://example.invalid/index"
[[package]]
name = "local"
version = "0.1.0"
`)
	exceptions := write("exceptions.json", `{"version":1,"crates":[{"name":"derive_api","label":"@vendor//:derive-package-1.0.0","proc_macro":true}]}`)
	index, err := loadCargoLock("@vendor", manifest, lock, exceptions)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(index.crates["wire_codec"]); got != 2 {
		t.Fatalf("lost version ambiguity: %d", got)
	}
	if got := index.crates["renamed"]; len(got) != 1 || got[0].label.String() != "@vendor//:wire-codec" {
		t.Fatalf("manifest did not select the default alias: %+v", got)
	}
	if got := index.byLabel["@vendor//:wire-codec-1.0.0"].label.String(); got != "@vendor//:wire-codec-1.0.0" {
		t.Fatalf("explicit version lost: %s", got)
	}
	if len(index.crates["derive_package"]) != 0 {
		t.Fatal("inferred name survived explicit library-name override")
	}
	c := index.crates["derive_api"][0]
	if !c.macro || c.label.String() != "@vendor//:derive-package-1.0.0" {
		t.Fatalf("lost macro exception: %+v", c)
	}
	if len(index.crates["local"]) != 0 {
		t.Fatal("workspace package became external")
	}
	if _, err := loadCargoLock("invalid", manifest, lock, exceptions); err == nil {
		t.Fatal("accepted invalid repository")
	}
	if _, err := loadCargoLock("@vendor", manifest, write("bad.lock", "version = 99"), ""); err == nil {
		t.Fatal("accepted unknown lock format")
	}
	collision := write("collision.lock", `version=4
[[package]]
name="wire"
version="1.0.0"
source="registry+https://one.invalid"
[[package]]
name="wire"
version="1.0.0"
source="sparse+https://two.invalid"
`)
	if _, err := loadCargoLock("@vendor", manifest, collision, ""); err == nil {
		t.Fatal("accepted conflicting registries")
	}
}

func TestCentralManifestVersionSelection(t *testing.T) {
	for _, tc := range []struct {
		name, manifest, imported string
		want                     []string
	}{
		{"direct", "[dependencies]\nwire = \"2\"", "wire", []string{"@vendor//:wire"}},
		{"inherited", "[workspace.dependencies]\nwire = \"2\"\n[dependencies]\nwire = { workspace = true }", "wire", []string{"@vendor//:wire"}},
		{"renamed", "[workspace.dependencies]\nrenamed = { package = \"wire\", version = \"2\" }", "renamed", []string{"@vendor//:wire"}},
		{"two_direct_versions", "[workspace.dependencies]\nwire = \"2\"\nlegacy = { package = \"wire\", version = \"1\" }", "legacy", []string{"@vendor//:wire-1.0.0"}},
		{"two_direct_versions_default", "[workspace.dependencies]\nwire = \"2\"\nlegacy = { package = \"wire\", version = \"1\" }", "wire", []string{"@vendor//:wire-2.0.0"}},
		{"no_match", "[dependencies]\nwire = \"3\"", "wire", nil},
		{"ambiguous_range", "[dependencies]\nwire = \">=1, <3\"", "wire", []string{"@vendor//:wire-1.0.0", "@vendor//:wire-2.0.0"}},
		{"transitive", "", "wire", []string{"@vendor//:wire-1.0.0", "@vendor//:wire-2.0.0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			manifest, lock := filepath.Join(dir, "Cargo.toml"), filepath.Join(dir, "Cargo.lock")
			if err := os.WriteFile(manifest, []byte(tc.manifest), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(lock, []byte(`version = 4
[[package]]
name = "wire"
version = "1.0.0"
source = "registry+https://example.invalid/index"
[[package]]
name = "wire"
version = "2.0.0"
source = "registry+https://example.invalid/index"
`), 0644); err != nil {
				t.Fatal(err)
			}
			index, err := loadCargoLock("@vendor", manifest, lock, "")
			if err != nil {
				t.Fatal(err)
			}
			got := index.candidates("consumer", tc.imported)
			if len(got) != len(tc.want) {
				t.Fatalf("candidates: %+v; want %v", got, tc.want)
			}
			for i, want := range tc.want {
				if got[i].label.String() != want {
					t.Fatalf("candidate: %s; want %s", got[i].label, want)
				}
			}
		})
	}
}

func TestCargoVersionRequirements(t *testing.T) {
	for _, tc := range []struct {
		requirement, version string
		match                bool
	}{
		{"1.2", "1.9.0", true}, {"1.2", "2.0.0", false},
		{"0.2", "0.3.0", false}, {"0.0.3", "0.0.4", false},
		{"~1.2", "1.3.0", false}, {"1.*", "1.9.0", true},
		{"=1.2.3", "1.2.4", false}, {">=1.2, <2", "1.9.0", true},
		{"1", "1.2.0-beta.1", false}, {"1.2.0-alpha", "1.2.0-beta.1", true},
		{"1.2.0-alpha", "1.3.0-beta.1", false}, {"1.2.0-alpha", "1.3.0", true},
		{"=1.2.3", "1.2.3+build.1", true},
	} {
		t.Run(tc.requirement+"/"+tc.version, func(t *testing.T) {
			got, err := cargoVersionMatches(tc.requirement, tc.version)
			if err != nil || got != tc.match {
				t.Fatalf("match = %v, %v; want %v", got, err, tc.match)
			}
		})
	}
	for _, tc := range [][2]string{{"", "1.0.0"}, {"1 || 2", "1.0.0"}, {"1", "bad"}} {
		if _, err := cargoVersionMatches(tc[0], tc[1]); err == nil {
			t.Fatalf("accepted invalid inputs: %v", tc)
		}
	}
}

func TestDirectGitDependencies(t *testing.T) {
	const source = "git+https://example.invalid/repo.git?tag=v1#0123456789abcdef0123456789abcdef01234567"
	for _, tc := range []struct {
		name, declaration, source, extra, imported string
		want                                       bool
	}{
		{"tag", `wire = { git = "https://example.invalid/repo.git", tag = "v1" }`, source, "", "wire", true},
		{"branch", `wire = { git = "https://example.invalid/repo.git", branch = "next" }`, "git+https://example.invalid/repo.git?branch=next#abc", "", "wire", true},
		{"rev", `wire = { git = "https://example.invalid/repo.git", rev = "abc" }`, "git+https://example.invalid/repo.git?rev=abc#abcdef", "", "wire", true},
		{"default", `wire = { git = "https://example.invalid/repo.git" }`, "git+https://example.invalid/repo.git#abc", "", "wire", true},
		{"renamed", `codec = { package = "wire", git = "https://example.invalid/repo.git", tag = "v1" }`, source, "", "codec", true},
		{"version_match", `wire = { git = "https://example.invalid/repo.git", tag = "v1", version = "1" }`, source, "", "wire", true},
		{"version_mismatch", `wire = { git = "https://example.invalid/repo.git", tag = "v1", version = "2" }`, source, "", "wire", false},
		{"wrong_repository", `wire = { git = "https://other.invalid/repo.git", tag = "v1" }`, source, "", "wire", false},
		{"wrong_tag", `wire = { git = "https://example.invalid/repo.git", tag = "v2" }`, source, "", "wire", false},
		{"wrong_ref_kind", `wire = { git = "https://example.invalid/repo.git", branch = "v1" }`, source, "", "wire", false},
		{"missing_revision", `wire = { git = "https://example.invalid/repo.git", tag = "v1" }`, "git+https://example.invalid/repo.git?tag=v1", "", "wire", false},
		{"registry_only", `wire = { git = "https://example.invalid/repo.git", tag = "v1" }`, "registry+https://example.invalid/index", "", "wire", false},
		{"same_name_registry", `wire = { git = "https://example.invalid/repo.git", tag = "v1" }`, source, "[[package]]\nname='wire'\nversion='2.0.0'\nsource='registry+https://example.invalid/index'\n", "wire", false},
		{"same_name_git", `wire = { git = "https://example.invalid/repo.git", tag = "v1" }`, source, "[[package]]\nname='wire'\nversion='2.0.0'\nsource='git+https://example.invalid/repo.git?tag=v2#def'\n", "wire", false},
		{"transitive_only", "", source, "", "wire", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			manifest, lock := filepath.Join(dir, "Cargo.toml"), filepath.Join(dir, "Cargo.lock")
			if err := os.WriteFile(manifest, []byte("[dependencies]\n"+tc.declaration), 0644); err != nil {
				t.Fatal(err)
			}
			contents := "version=4\n[[package]]\nname='wire'\nversion='1.0.0'\nsource='" + tc.source + "'\n" + tc.extra
			if err := os.WriteFile(lock, []byte(contents), 0644); err != nil {
				t.Fatal(err)
			}
			index, err := loadCargoLock("@vendor", manifest, lock, "")
			if err != nil {
				t.Fatal(err)
			}
			got := index.candidates("consumer", tc.imported)
			if !tc.want {
				if len(got) != 0 {
					t.Fatalf("guessed a Git dependency: %+v", got)
				}
			} else if len(got) != 1 || got[0].label.String() != "@vendor//:wire" || got[0].name != "wire" {
				t.Fatalf("wrong Git dependency: %+v", got)
			}
		})
	}
}

func TestGitCatalogOverride(t *testing.T) {
	dir := t.TempDir()
	manifest, lock, catalog := filepath.Join(dir, "Cargo.toml"), filepath.Join(dir, "Cargo.lock"), filepath.Join(dir, "catalog.json")
	for path, content := range map[string]string{
		manifest: "[dependencies]\nwire = { git = 'https://example.invalid/repo.git', tag = 'v1' }",
		lock:     "version=4",
		catalog:  `{"version":1,"crates":[{"name":"wire","label":"@custom//:wire","proc_macro":true}]}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	index, err := loadCargoLock("@vendor", manifest, lock, catalog)
	if err != nil {
		t.Fatal(err)
	}
	got := index.candidates("consumer", "wire")
	if len(got) != 1 || got[0].label.String() != "@custom//:wire" || !got[0].macro {
		t.Fatalf("lost explicit Git override: %+v", got)
	}
}

func TestGitPatchedRegistryDependency(t *testing.T) {
	for _, tc := range []struct {
		name, manifest, source string
		want                   bool
	}{
		{"selected", "[dependencies]\nwire='1'\n[patch.crates-io]\nwire={git='https://example.invalid/wire',rev='abc'}", "git+https://example.invalid/wire?rev=abc#abcdef", true},
		{"inherited", "[workspace.dependencies]\nwire='1'\n[dependencies]\nwire={workspace=true}\n[patch.crates-io]\nwire={git='https://example.invalid/wire',rev='abc'}", "git+https://example.invalid/wire?rev=abc#abcdef", true},
		{"patch_alias", "[dependencies]\nwire='1'\n[patch.crates-io]\npatched={package='wire',git='https://example.invalid/wire',rev='abc'}", "git+https://example.invalid/wire?rev=abc#abcdef", true},
		{"unused_patch", "[dependencies]\nwire='1'\n[patch.crates-io]\nwire={git='https://example.invalid/wire',rev='abc'}", "registry+https://example.invalid/index", true},
		{"wrong_revision", "[dependencies]\nwire='1'\n[patch.crates-io]\nwire={git='https://example.invalid/wire',rev='wrong'}", "git+https://example.invalid/wire?rev=abc#abcdef", false},
		{"direct_constraint", "[dependencies]\nwire='2'\n[patch.crates-io]\nwire={git='https://example.invalid/wire',rev='abc'}", "git+https://example.invalid/wire?rev=abc#abcdef", false},
		{"patch_constraint", "[dependencies]\nwire='1'\n[patch.crates-io]\nwire={version='2',git='https://example.invalid/wire',rev='abc'}", "git+https://example.invalid/wire?rev=abc#abcdef", false},
		{"custom_registry", "[dependencies]\nwire={version='1',registry='private'}\n[patch.crates-io]\nwire={git='https://example.invalid/wire',rev='abc'}", "git+https://example.invalid/wire?rev=abc#abcdef", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			manifest, lock := filepath.Join(dir, "Cargo.toml"), filepath.Join(dir, "Cargo.lock")
			if err := os.WriteFile(manifest, []byte(tc.manifest), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(lock, []byte("version=4\n[[package]]\nname='wire'\nversion='1.0.0'\nsource='"+tc.source+"'"), 0644); err != nil {
				t.Fatal(err)
			}
			index, err := loadCargoLock("@vendor", manifest, lock, "")
			if err != nil {
				t.Fatal(err)
			}
			got := index.candidates("consumer", "wire")
			if tc.want {
				if len(got) != 1 || got[0].label.String() != "@vendor//:wire" {
					t.Fatalf("wrong patch resolution: %+v", got)
				}
			} else if len(got) != 0 {
				t.Fatalf("accepted unmatched patch: %+v", got)
			}
		})
	}
}
