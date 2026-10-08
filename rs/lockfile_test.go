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
`)
	lock := write("Cargo.lock", `version = 4
[[package]]
name = "wire-codec"
version = "1.0.0"
source = "registry+https://example.invalid/index"
[[package]]
name = "wire-codec"
version = "2.0.0"
source = "registry+https://example.invalid/index"
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
	if got := len(index.crates["renamed"]); got != 2 {
		t.Fatalf("lost renamed candidates: %d", got)
	}
	if _, ok := index.byLabel["@vendor//:wire-codec"]; ok {
		t.Fatal("ambiguous unversioned alias")
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
source="registry+https://two.invalid"
`)
	if _, err := loadCargoLock("@vendor", manifest, collision, ""); err == nil {
		t.Fatal("accepted conflicting registries")
	}
}
