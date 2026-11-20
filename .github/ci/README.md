# CI

GitHub Actions drives Bazel 8/9 tests, examples, and cross-builds on BuildBuddy.
Linux clients use standard runners; BuildBuddy runs compilation. `CI` fails if
any job fails or skips.

`BB_RO_KEY` is a public Actions variable, available to fork PRs. Its BuildBuddy
permissions must allow CAS reads/writes and AC reads, without AC writes
(Developer permissions, not a strictly read-only Reader key). PRs, merge queues,
manual runs, and artifact downloads use this key. Local-result uploads default
off; the server-side key permissions enforce the AC boundary.

`BB_RW_KEY` is the private writer key for post-merge pushes to `main`. Store it
as a secret in the `ci` environment, whose deployment branch policy allows only
`main`. Remove any repository/organization-wide writer-key exposure to PR jobs.
Both the workflow and Bazel wrapper restrict writer use; an overridden checkout
in reusable CI uses the public key. Trusted main builds fail if the writer key
is missing.

Cross-builds produce Linux, Darwin, and Windows archives on Linux executors.
Only invocation metadata passes from build jobs to download jobs. Download jobs
verify successful BuildBuddy invocations and fetch archives through `GetFile`,
then relay binaries through GitHub artifacts to native smoke jobs. Native jobs
run downloaded binaries without installing Bazel or compiling.

Downloads verify commit, run attempt, repository, platform, and SHA-256. Repository
caches stay local; action results use BuildBuddy. Bazel 9 repository contents
caches stay disabled because LLVM repositories can retain absolute symlinks.

Releases require successful main CI at the exact tagged commit. Publishing stays
in GitHub Actions. Releases package Git source with `git archive`; they do not
compile with Bazel, receive BB credentials, or read the shared action cache.
Reusable release workflow caches stay disabled. If compiled release artifacts
are added, use a separate BuildBuddy organization and release key, never the CI
key or its action cache.
