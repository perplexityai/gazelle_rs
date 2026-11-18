# CI

GitHub Actions drives Bazel 8/9 tests, examples, and cross-builds on BuildBuddy
using `BB_RW_KEY`. Linux clients use standard runners; BuildBuddy runs compilation.
`CI` fails if any job fails or skips.

Cross-builds produce Linux, Darwin, and Windows archives on Linux executors.
Only invocation metadata passes from build jobs to download jobs. Download jobs
verify successful BuildBuddy invocations and fetch archives through `GetFile`,
then relay binaries through GitHub artifacts to native smoke jobs. Native jobs
run downloaded binaries without installing Bazel or compiling.

Downloads verify commit, run attempt, repository, platform, and SHA-256. Repository
caches stay local; action results use BuildBuddy. Bazel 9 repository contents
caches stay disabled because LLVM repositories can retain absolute symlinks.

Releases require successful main CI at the exact tagged commit. Publishing stays
in GitHub Actions. Reusable CI callers must pass `BB_RW_KEY`; cross-builds require
it. Tests and examples can still run locally when the key is unavailable.
