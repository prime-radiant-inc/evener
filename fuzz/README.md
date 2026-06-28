# serf fuzzing + failure-to-regression toolkit

This module (`primeradiant.com/serf/fuzz`) is the **serf-agnostic core** of the
toolkit: the failure→regression promoter (and, later, the schema→generator).
Nothing here imports any `primeradiant.com/serf` package — `go.mod` declares no
serf dependency, so the module will not build if that boundary is violated. That
structural guarantee *is* the portability test (the eventual superpowers skill
carries this module unchanged).

The per-surface fuzz **targets** do NOT live here — Go requires `testing.F`
targets to sit in the package under test (they call unexported seams and their
`testdata/fuzz/` corpus lives beside them). They are:

| Surface | Target | File |
|---|---|---|
| SSE frame parser | `FuzzParseSSE` | `llm/sse_fuzz_test.go` |
| AppWire frame decode | `FuzzMessageDecode` | `appwire/jsonrpc_fuzz_test.go` |
| AppWire per-method params | `FuzzMethodParams` | `appwire/params_fuzz_test.go` |
| Tool-arg decode+validate | `FuzzToolArgsValidate` | `agent/tool_args_fuzz_test.go` |

## Running

```sh
make fuzz           # seed corpus + saved crashers as deterministic tests (gate-safe)
make fuzz-nightly   # bounded coverage-guided search per target (60s each by default)
scripts/run-fuzz.sh --time 5m            # all targets, 5 min each
scripts/run-fuzz.sh llm:FuzzParseSSE     # one target
```

`make fuzz` runs `go test -run '^Fuzz' ./...` per module: with no `-fuzz` flag
the toolchain does **not** random-search — it replays each target's seed corpus
plus any saved `testdata/fuzz/<FuzzName>/` crashers as ordinary, deterministic
unit tests. `make test` already executes these seeds (Go runs Fuzz seed corpora
as subtests), so the gate covers them; `make fuzz` is the explicit entry point.

## The free regression loop

When `make fuzz-nightly` (or `go test -fuzz`) finds a failing input, the Go
toolchain writes it to that target's `testdata/fuzz/<FuzzName>/<hash>`. That file
is committed and re-runs forever as a deterministic regression test — no promoter
needed for single-input `testing.F` targets. The promoter in `promoter/` exists
for the surfaces Go does *not* auto-promote (stateful sequences, non-`testing.F`
runners).

## Oracles (never bare "no panic")

- **SSE** — chunk-invariance (the parser must yield identical events whether fed
  one byte at a time or in one buffer) plus blocking-vs-timeout path agreement.
- **AppWire frame / params** — decode→encode is idempotent after one
  normalization pass (catches codec instability that "no panic" misses).
- **Tool args** — panic-hunt on the `Schema.Validate` seam, which is *not*
  recover-wrapped at runtime. The target drives decode+validate only; it does not
  execute tools (shell/web/job are non-deterministic and unsandboxable by a
  temp-dir env).

## Seed corpus

Seeds are inline `f.Add(...)` calls in each target (the Go-native convention):
canonical edge values plus OWASP-style adversarial shapes (wrong types, deep
nesting, huge numbers, invalid UTF-8, lone surrogates). `corpus/` is reserved for
cross-surface OWASP fuzz-vector files shared by the HTTP surface in a later phase.
