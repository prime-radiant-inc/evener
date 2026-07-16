# Aggregate Quiet Lint Design

Date: 2026-07-15
Status: Approved
Tracker: #20

## Purpose

The Go workspace contains multiple modules. `lint-golangci` currently runs them
serially and stops at the first failure, requiring repeated long runs to discover
independent problems. Successful lint runs also should not flood the terminal
with non-actionable output.

`make lint` will produce one complete diagnostic pass and remain quiet when
everything is healthy.

## User-Visible Contract

A successful run prints only compact orchestration status:

```text
lint: checking 7 modules
PASS lint (7 modules, 18.4s)
```

Exact timing formatting may follow repository conventions. It must not print
per-module successful linter output.

A failing run:

- executes every Go module's `golangci-lint` check;
- captures stdout/stderr separately per check;
- prints complete captured output for failed checks only;
- ends with one summary naming every failed module/check;
- exits nonzero if any check failed.

## Runner Design

Add a dedicated shell runner rather than expanding a large Make recipe. The
runner follows the proven structure of `scripts/run-module-tests.sh`:

- module list comes from `MODULES`, defaulting to the repository's non-fuzz Go
  modules;
- modules run in a bounded parallel wave;
- each module writes to a private temporary log;
- the parent waits for every child and records all failures;
- success logs are deleted at exit;
- failure output is replayed deterministically in module-list order;
- interrupted runs terminate/wait for children and clean temporary logs.

The default module set remains:

```text
. agent llm auth envvars invariant identifier
```

Expose one environment variable for bounded lint parallelism. The default must
avoid oversubscribing ordinary developer and CI machines; it must not simply
launch unbounded work for every future module.

## `make lint`

`make lint` keeps the existing lint coverage:

- naming;
- internal dependency rules;
- docs;
- golangci-lint for every non-fuzz module;
- generated-file verification;
- secret scan.

The new runner owns the module aggregation. Other successful checks remain
silent. If an existing check prints routine success chatter, capture/suppress it
while preserving complete failure output.

Do not change linter versions, flags, enabled checks, generation behavior,
module membership, or secret-scan policy in this project.

## Failure Semantics

- A missing `golangci-lint` executable is reported for every module only once in
  the terminal summary, with its original diagnostic retained.
- A module failure does not prevent later modules from running.
- A setup failure that makes all module runs impossible still yields a bounded,
  non-duplicated diagnostic and nonzero exit.
- Temporary-log creation failure stops before launching checks and returns
  nonzero.
- Signal interruption returns nonzero and does not leave child linters running.

## Testing

Test the executable runner with fake commands and temporary modules. Do not test
rendered shell source or Make command strings with large regex assertions.

Cover:

- all-success output has one start line and one final PASS line;
- success command chatter is absent;
- multiple failing modules all run and all failure logs appear;
- failure summary order follows `MODULES`;
- mixed pass/fail logs print only failures;
- exit status is nonzero on any failure;
- concurrency never exceeds the configured bound;
- interruption cleans child processes and logs;
- Makefile wiring passes the canonical module list;
- the repository's real lint command still exercises every existing lint family.

## Scope Lock

This spec does not:

- alter lint rules, tool versions, or module coverage;
- change `make test`, race, fuzz, or live-test gates;
- add a new build system;
- cache lint results;
- hide failure output;
- print per-module success lines;
- modify production Serf behavior.
