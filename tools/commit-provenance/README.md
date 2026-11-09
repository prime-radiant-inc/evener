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

GitHub transport has not yet been exercised by this prototype. A live test needs
an explicitly chosen disposable repository. It should publish only synthetic
metadata and a tiny fixture, push a new branch, clone into a fresh directory,
and compare the raw commit bytes and object ID. GitHub-created squash/rebase
commits need a separate test; ordinary transport does not establish their
metadata preservation.

## Design

The [proposed design](../../docs/superpowers/specs/2026-11-09-commit-provenance-design.md)
separates the proven storage mechanism from the unresolved production insertion
and rewrite policy. No runtime behavior has changed.
