// Driver for fixture-style gazelle tests.
//
// Each subdirectory of testdata/ is treated as one test case. We run the
// project's gazelle_binary inside it and compare the generated BUILD.bazel
// against BUILD.out, plus optional expected{Stdout,Stderr,ExitCode}.txt files.
// Heavy lifting lives in `bazel-gazelle/testtools.TestGazelleGenerationOnPath`,
// which is the same harness gazelle uses for its own language plugins.
//
// Conventions match rules_python's gazelle/rsthon/testdata so existing fixture
// authors recognize the layout, but the runner uses gazelle's stock helper
// instead of rules_python's hand-rolled python_test.go.
package gazelle_test_test

import (
	"bytes"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bazelbuild/bazel-gazelle/testtools"
	"github.com/bazelbuild/rules_go/go/runfiles"
)

var gazelleBinaryPath = flag.String(
	"gazelle_binary",
	"",
	"rlocationpath of the gazelle_binary under test (passed in by the gazelle_tests macro).",
)

// testdataRelative is where fixtures live relative to the workspace root. Used
// only for the UPDATE_SNAPSHOTS hint testtools prints on failure.
const testdataRelative = "rs/gazelle_test/testdata"

func currentRepoRunfile(relativePath string) (string, error) {
	repo := runfiles.CurrentRepository()
	runfilesDir := repo
	if runfilesDir == "" {
		runfilesDir = "_main"
	}
	return runfiles.RlocationFrom(path.Join(runfilesDir, relativePath), repo)
}

func TestFixtures(t *testing.T) {
	if *gazelleBinaryPath == "" {
		t.Fatal("-gazelle_binary flag is required (set by the gazelle_tests macro)")
	}
	gazelleBin, err := runfiles.Rlocation(*gazelleBinaryPath)
	if err != nil {
		t.Fatalf("resolve gazelle binary: %v", err)
	}

	testdataRoot, err := currentRepoRunfile(testdataRelative)
	if err != nil {
		t.Fatalf("resolve testdata root: %v", err)
	}

	entries, err := os.ReadDir(testdataRoot)
	if err != nil {
		t.Fatalf("read testdata root %q: %v", testdataRoot, err)
	}

	any := false
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		any = true
		testtools.TestGazelleGenerationOnPath(t, &testtools.TestGazelleGenerationArgs{
			Name:                 e.Name(),
			TestDataPathAbsolute: filepath.Join(testdataRoot, e.Name()),
			TestDataPathRelative: path.Join(testdataRelative, e.Name()),
			GazelleBinaryPath:    gazelleBin,
			Timeout:              30 * time.Second,
		})
		t.Run(e.Name()+"_idempotent", func(t *testing.T) {
			root := t.TempDir()
			fixture := filepath.Join(testdataRoot, e.Name())
			err := filepath.WalkDir(fixture, func(file string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() || strings.HasSuffix(file, ".in") {
					return nil
				}
				rel, err := filepath.Rel(fixture, file)
				if err != nil {
					return err
				}
				if strings.HasSuffix(rel, ".out") {
					rel = strings.TrimSuffix(rel, ".out") + ".bazel"
				}
				target := filepath.Join(root, rel)
				if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
					return err
				}
				data, err := os.ReadFile(file)
				if err != nil {
					return err
				}
				return os.WriteFile(target, data, 0644)
			})
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(gazelleBin, "-repo_root="+root, "-mode=diff", root)
			cmd.Dir = root
			expectedStderr, err := os.ReadFile(filepath.Join(fixture, "expectedStderr.txt"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if output, err := cmd.Output(); err != nil || len(output) > 0 || stderr.String() != string(expectedStderr) {
				t.Fatalf("second run: %v\nstdout: %s\nstderr: %s", err, output, stderr.String())
			}
		})
	}
	if !any {
		t.Fatalf("no fixture directories under %q", testdataRoot)
	}
}

func TestStrictDiagnostics(t *testing.T) {
	gazelleBin, err := runfiles.Rlocation(*gazelleBinaryPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, directive, source, diagnostic string
	}{
		{"unresolved", "", "pub use missing_crate::Value;", "unresolved crate"},
		{"parse", "", "pub fn broken( {", "leaving target unchanged"},
		{"configuration", "# gazelle:rust_edition invalid\n", "pub fn valid() {}", "invalid Rust edition"},
	} {
		for _, strict := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/strict=%v", tc.name, strict), func(t *testing.T) {
				root := t.TempDir()
				build := tc.directive + `load("@rules_rs//rs:rust_library.bzl", "rust_library")

rust_library(
    name = "sample",
    srcs = ["lib.rs"],
    crate_root = "lib.rs",
    deps = ["//existing:dependency"],
)
`
				for name, contents := range map[string]string{"MODULE.bazel": "", "lib.rs": tc.source, "BUILD.bazel": build} {
					if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0644); err != nil {
						t.Fatal(err)
					}
				}
				cmd := exec.Command(gazelleBin, "-repo_root="+root, fmt.Sprintf("-strict=%v", strict), root)
				cmd.Dir = root
				output, err := cmd.CombinedOutput()
				if (err != nil) != strict || !strings.Contains(string(output), tc.diagnostic) {
					t.Fatalf("exit: %v; output: %s", err, output)
				}
				got, err := os.ReadFile(filepath.Join(root, "BUILD.bazel"))
				if err != nil {
					t.Fatal(err)
				}
				if strict && string(got) != build {
					t.Fatalf("strict failure modified BUILD: %s", got)
				}
				if tc.name != "configuration" && !strings.Contains(string(got), "//existing:dependency") {
					t.Fatalf("incomplete inference removed existing dependency: %s", got)
				}
			})
		}
	}
}
