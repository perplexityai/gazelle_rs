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
