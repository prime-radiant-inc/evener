# Commit provenance prototype

This experiment puts session breadcrumbs in a Git commit's extra headers. It is
not installed, does not instrument Evener sessions, and does not promise to
annotate every commit.

## Run

From the repository root:

```sh
go run ./tools/commit-provenance -h
go run ./tools/commit-provenance \
  -repo /path/to/disposable/repository -commit HEAD \
  -evener-version fixture-version \
  -model fixture-provider/fixture-model \
  -host fixture-host -session fixture-session
```

All four fields are explicit. The prototype must not guess which session or
model did the work. Host and session metadata will be readable by anyone who
can read a published commit. Use synthetic values for experiments.

The repository argument controls Git discovery. Inherited repository-selection
variables such as `GIT_DIR` and `GIT_OBJECT_DIRECTORY` are filtered using Evener's
shared helper so they cannot redirect the experiment into another repository.

The command prints a **new, unreferenced object ID**. It leaves the source
commit, branches, index, working files, Git configuration and hooks unchanged.
Inspect the result with `git cat-file -p <printed-id>`. Unreferenced objects are
subject to garbage collection. Only in a disposable repository, attach one to
a test branch with `git update-ref refs/heads/provenance-probe <printed-id>`.
That branch, not a separate notes ref, carries the metadata on push.

The command refuses a signed source commit because changing its bytes would
invalidate its signature. It does not remove signatures, sign with a new key,
rewrite history, or claim that hooks ran on the newly written object.

## Candidate representation

A repeated, single-line `evener-provenance` header holds one JSON record:

```text
evener-provenance {"schema":1,"evener_version":"fixture-version","model":"fixture-provider/fixture-model","host":"fixture-host","session_id":"fixture-session"}
```

The field lives before the blank line separating commit headers from the
message. It participates in the object hash and travels with the object.
Original headers and message bytes stay intact. Identical records emitted by
this prototype are idempotent; different records append rather than replacing
earlier session breadcrumbs. This is self-reported provenance, not proof of
identity or a complete account of authorship.

## Repeat the local experiments

```sh
GOMAXPROCS=2 go test -p 1 ./tools/commit-provenance -count=1 -v
GOMAXPROCS=2 go vet ./tools/commit-provenance
```

Set `TMPDIR="$EVENER_SCRATCH_DIR"` when running inside an Evener session. Tests
use disposable real Git repositories and isolate Git configuration. No network,
provider credentials, fake Git executable or product build is involved. Git and
`ssh-keygen` are required. The SHA-256 experiment needs a Git version supporting
that object format. Tests clean up their repositories and ephemeral signing key.

| Experiment on Git 2.43.0 | Observed result |
| --- | --- |
| Strict object checking | Header-bearing commit accepted |
| Push to local bare remote with `receive.fsckObjects=true`, fresh clone through pack transport | Exact object ID and bytes preserved, no notes ref |
| Native amend | Extra header retained on the new commit |
| Native rebase | Extra header dropped from replayed commit |
| Native cherry-pick | Extra header dropped from new commit |
| Native squash merge and commit | Extra header absent from squash commit |
| Native non-fast-forward merge | Source object remains reachable, new merge commit has no header |
| Fast-forward merge | Original header-bearing object remains the tip |
| Stamp an ordinary commit with existing hooks | Existing hook configuration and execution history unchanged |
| Rewrite HEAD from a real post-commit hook | Git reports the old object ID while HEAD points to the new one |
| Insert header into a real SSH-signed commit | Signature verification fails; prototype refuses this operation |
| Record validation and repeat stamping | Invalid records refused, identical record idempotent, other records retained |
| Existing unknown headers and non-UTF-8 message | Original bytes preserved |
| SHA-256 object storage | Header-bearing object accepted locally |
| Explicit repository with conflicting inherited Git routing | Requested repository used, unrelated object database unchanged |

The rewrite matrix is an experiment, not an assertion that losing metadata is
acceptable product behavior. It identifies where a production integration must
intervene. Tests pin the actual operation: changed commit identity and retained
file content, not just the absence of a header.

## GitHub evidence

The live experiment passed on 2026-11-09 in the private
[`obra/evener-provenance-probe`](https://github.com/obra/evener-provenance-probe)
repository. It published only synthetic metadata and two tiny fixture files.

| GitHub operation | Observed result |
| --- | --- |
| HTTPS push and fresh clone | Exact source commit ID and complete raw bytes preserved |
| Git commit API | No top-level field exposing the custom header |
| [Regular merge](https://github.com/obra/evener-provenance-probe/pull/1) | Merge tip has no record, original annotated commit remains reachable |
| [Squash merge](https://github.com/obra/evener-provenance-probe/pull/2) | New commit has no record, original commit absent from target ancestry |
| [Rebase merge](https://github.com/obra/evener-provenance-probe/pull/3) | New commit retains the record, original commit absent from target ancestry |

**GitHub rebase differed from native Git 2.43.0 rebase.** A second fresh clone
and independent byte-level comparison confirmed the rebased provenance header
is identical to the source. This is an observed unsigned, single-commit fixture,
not a guarantee for every GitHub rewrite path or future implementation.

The retained source branch is `probe-dmqf9uksgcsz-source`, at
`3e5cc7d55a9bd7c5b72a866fe320e8ba44198e25`. Result branches share that prefix:
`-merge-base` at `9247421b60bebebdcd61198bff310aed9360e6a5`,
`-squash-base` at `b20e3382862ba11a1f633ad6859400523b271c8c`, and
`-rebase-base` at `a864ac7bb1da3c1a2ca9083a693ddcd86cc167f1`.
After an ordinary authenticated clone, inspect each with
`git cat-file -p <object-id>` and its ancestry with `git rev-list <object-id>`.

The live harness requires an authorized **private** repository and explicit
opt-in. It creates uniquely prefixed fixture branches, opens and merges three
test pull requests using merge, squash and rebase, and leaves all remote objects
for inspection. It never updates a pre-existing branch. Default tests skip it.
Run from the repository root after creating the disposable destination:

```sh
GH_CONFIG_DIR="$HOME/.config/gh" \
EVENER_PROVENANCE_GITHUB_E2E=1 \
EVENER_PROVENANCE_GITHUB_REPO=obra/evener-provenance-probe \
TMPDIR="$EVENER_SCRATCH_DIR" GOMAXPROCS=2 \
go test -p 1 ./tools/commit-provenance -run '^TestGitHubProvenance$' -count=1 -v
```

Use the actual absolute `gh` configuration directory for `GH_CONFIG_DIR`. The
fixture isolates `HOME` and Git configuration, while the credential helper
delegates HTTPS authentication to `gh`. No token is printed or stored in a URL.

## Design

The [proposed design](../../docs/superpowers/specs/2026-11-09-commit-provenance-design.md)
separates the proven storage mechanism from the unresolved production insertion
and rewrite policy. No runtime behavior has changed.
