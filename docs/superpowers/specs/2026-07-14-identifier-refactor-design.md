# Unified Project and Generated Identifier Design

**Date:** 2026-07-14
**Status:** Approved for planning
**Scope:** Project identifiers and every Serf-owned ULID-backed identifier

## Summary

Serf currently derives project identifiers through four unrelated algorithms:

- runtime state buckets hash a Git origin URL or working directory;
- hub launch configuration hashes a canonical repository path;
- managed worktrees use a sanitized basename plus a path hash;
- hub project keys use a basename plus a shorter path hash.

Serf also mints 26-character ULIDs at separate call sites for sessions, forks, jobs, delegates, watches, generations, deliveries, installation identity, agent calls, and synthetic provider calls.

This design replaces those implementations with one dependency-light `identifier` module. Its project API owns path canonicalization, including symlink and Git linked-worktree resolution. Its generated-ID API encodes UUIDv7 values as fixed-width, 22-character base62 strings. Existing domain prefixes remain unchanged.

This release makes a clean break from the old formats. Serf neither migrates nor reads old hash-named project state or old ULID-named local artifacts. It leaves old files untouched.

## Goals

1. Provide one implementation for project identity across the repository.
2. Produce readable, filesystem-safe project IDs with a stable maximum length.
3. Aggregate a Git main checkout and all its linked worktrees under one project.
4. Replace every Serf-owned ULID payload with a shorter, fixed-width encoding.
5. Preserve collision resistance, chronological ordering, and domain prefixes.
6. Make format and single-codepath claims enforceable through tests and audits.

## Non-goals

- Migrate or rename existing project buckets, sessions, transcripts, logs, or durable references.
- Preserve compatibility with old project hashes or 26-character ULIDs.
- Make project IDs reversible. The canonical path remains authoritative.
- Normalize external provider or thread identifiers. Serf treats them as opaque strings.
- Merge distinct clones that occupy different canonical main-checkout paths.

## Package Architecture

Add a leaf module:

```text
primeradiant.com/serf/identifier
```

All repository modules may depend on it. It depends only on the standard library and the selected UUID implementation. It must not import the root, `agent`, or `llm` modules.

Use separate files and focused APIs:

- `project.go`: project resolution, rendering, hashing, and validation;
- `uuid.go`: UUIDv7 generation and the fixed-width base62 codec;
- `domains.go`: named constructors and validators for Serf ID domains.

These families share one module because both define repository-wide identifier policy and use the same safe alphabet. Their implementations remain independent: project IDs do not depend on UUID generation, and generated IDs do not depend on paths.

Delete the old project-ID implementations rather than retaining wrappers. Delete direct Serf-owned ULID generation sites. Repository audits will prevent either pattern from returning.

## Project Identity

### Public API

The normal API accepts a path and owns the entire normalization pipeline:

```go
type Project struct {
    ID            string
    CanonicalPath string
}

func ResolveProject(path string) (Project, error)
func ProjectID(path string) (string, error)
```

`ProjectID` wraps `ResolveProject` and returns its `ID`.

Some agent paths resolve through an execution environment rather than the host filesystem. The module therefore exposes an environment adapter without surrendering pipeline ownership:

```go
type Resolver interface {
    Abs(path string) (string, error)
    EvalSymlinks(path string) (string, error)
    MainCheckout(path string) (root string, isGit bool, err error)
}

func ResolveProjectWith(path string, resolver Resolver) (Project, error)
```

The exact interface may change during planning if existing environment abstractions require a different shape. The invariant may not change: the `identifier` module controls the pipeline order, validation, rendering, and hashing. Resolvers perform only environment-specific I/O.

Do not export a renderer that accepts an allegedly canonical string. Ordinary callers must not bypass canonicalization.

### Canonicalization Pipeline

`ResolveProject` and `ResolveProjectWith` perform these steps:

1. Reject an empty path.
2. Make the path absolute and clean it.
3. Resolve symlinks.
4. Detect whether the resolved path lies inside a Git repository.
5. For Git paths, resolve the repository's canonical main checkout. A linked worktree resolves to its owning main checkout. A submodule remains a distinct repository and resolves to its own checkout root.
6. Resolve symlinks and clean the resulting identity path again.
7. Render the project ID from that exact canonical path.
8. Return the ID and canonical path together.

If the API detects Git but cannot determine a stable main checkout, it returns an error. It must not hash the unresolved working directory as a fallback.

For non-Git directories, the canonical absolute, symlink-resolved directory is the identity path.

### Project ID Format

A project ID has this shape:

```text
<readable-path-tail>-<10-character-base62-hash>
```

Rules:

1. Split the canonical path into components and omit filesystem-root syntax.
2. In each component, preserve ASCII letters and digits.
3. Replace each run of all other characters with one hyphen.
4. Trim hyphens from each component and omit empty components.
5. Join components with hyphens.
6. Use `project` if no readable characters remain.
7. Compute SHA-256 over the exact canonical path and encode a uniform 10-character base62 suffix.
8. If the full ID would exceed 80 ASCII characters, remove characters from the left of the readable portion. Trim any newly exposed hyphen. Never truncate the suffix.
9. Do not pad short project IDs.

Example shape:

```text
/Users/jesse/git/prime-radiant/serf
→ Users-jesse-git-prime-radiant-serf-<10 chars>
```

The suffix provides about 59.5 bits of collision resistance. It distinguishes paths that sanitize to the same text and paths that share the same retained tail. The ID is recognizable but not reversible.

### Authority and Safety

`Project.CanonicalPath` is authoritative. Persist it wherever destructive operations, spawn/resume behavior, or display paths require a path.

A project ID alone never authorizes deletion. Destructive operations re-resolve the supplied or stored path, recompute the project identity, compare IDs, and enforce containment rules.

`ValidateProjectID` checks syntax only:

- total length is at most 80 ASCII characters;
- the alphabet contains only ASCII letters, digits, and hyphens;
- a non-empty readable portion precedes the final separator;
- exactly 10 base62 characters follow it.

## Generated Identifiers

### Payload

Every Serf-owned generated identifier uses a UUIDv7 payload encoded as a fixed-width base62 string.

- UUID payload: 128 bits, RFC 9562 version 7, RFC 4122 variant.
- Base62 alphabet: `0-9A-Za-z` in ASCII lexical order.
- Numeric interpretation: one unsigned, big-endian 128-bit integer.
- Width: exactly 22 characters, padded on the left with `0`.

Twenty-one base62 characters cannot represent every 128-bit value. Twenty-two characters can. Base64url is also 22 characters, so it does not shorten the result. Base62 avoids punctuation and preserves numeric and UUIDv7 time order under ordinary string comparison.

### Codec

The shared codec provides fixed-width encode and strict decode operations. Decoding rejects:

- a length other than 22;
- characters outside the base62 alphabet;
- values greater than `2^128 - 1`;
- invalid UUID version or variant bits when a UUIDv7 payload is required.

Fixed test vectors define the alphabet, width, byte order, padding, overflow behavior, and lexical-order contract.

### Generation and Errors

Use a cryptographically secure, process-monotonic UUIDv7 implementation. Entropy failure must not fall back to timestamps, counters, weak randomness, or partial IDs.

APIs that already return errors, including session and fork creation, propagate generation errors. Existing no-error factories expose explicit `MustNew...` wrappers that panic on entropy failure, preserving their current fail-fast behavior.

### Domains

Named constructors and validators preserve current prefixes while replacing each 26-character ULID payload with a 22-character base62 UUIDv7 payload.

| Domain | New shape |
|---|---|
| Session | `<payload>` |
| Installation | `<payload>` |
| Job | `job_<payload>` |
| Delegate | `dlg_<payload>` |
| Delegate generation | `dg_<payload>` |
| Watch | `watch_<payload>` |
| Watch generation | `wg_<payload>` |
| Watch delivery | `wd_<payload>` |
| Agent call | `ag_<payload>` |
| Synthetic provider call | `call_<payload>` |
| Unprefixed internal notification/delivery IDs | `<payload>` |

The implementation plan must inventory every production `ulid.Make` or `ulid.New` call before editing. If a Serf-owned domain is missing from this table, add a named constructor rather than calling the payload generator directly.

External provider and thread IDs remain opaque. Local-route validation must distinguish Serf-local IDs from external IDs rather than applying the local UUIDv7 format globally.

## Data Flow

### Project Consumers

Each project entry point resolves once and carries the returned `Project` value:

- runtime state: `<state-home>/serf/projects/<Project.ID>`;
- launch configuration and trust metadata: `<state-root>/projects/<Project.ID>`;
- managed worktrees: `<worktree-root>/<Project.ID>/<worktree-name>`;
- hub grouping, archive keys, deletion keys, and local project URLs: `Project.ID`;
- display, spawn, resume, and destructive path checks: `Project.CanonicalPath`.

Git origin URLs no longer participate in project identity. Two clones at different canonical paths are distinct projects. A main checkout and all its linked worktrees are one project.

Replace these existing implementations and their callers:

- runtime state bucket hashing in `agent/runtime_dir.go`;
- launch-config hashing in `cmd/serf-hub/internal/launchconfig/paths.go`;
- managed-worktree project IDs in `agent/internal/worktree/name.go`;
- hub project slugs in `cmd/serf-hub/internal/hubcore/tree.go`.

The implementation plan must search again from the target commit and include any new project-key producers added after this design.

### Generated-ID Consumers

All Serf-owned generation sites call named constructors from the shared module. At minimum, update:

- fresh sessions in `agent/session_init.go`;
- forked sessions in `agent/fork.go`;
- job, delegate, watch, generation, and delivery IDs in `agent/internal/jobstore`;
- installation IDs in `agent/internal/installid`;
- agent-call IDs in `agent/session_model_call.go`;
- synthetic Google provider call IDs in `llm/providers/google`.

Durable schemas remain strings. Only the shape of newly generated values changes.

## Clean Break

This change deliberately provides no legacy compatibility.

After upgrade:

- old hash-named project buckets are not current projects;
- old launch, trust, and managed-worktree state is not reused;
- old 26-character ULID sessions and generated-ID artifacts are unsupported;
- new state uses only the new project and generated-ID formats.

Do not add:

- legacy bucket discovery;
- dual-format validators;
- on-read renames;
- bulk migration;
- durable cross-reference rewrites;
- fallback lookup by old hash or ULID.

Serf must not delete old state. It remains inert on disk for manual inspection or removal.

The singleton installation ID is the exception to inert historical data: if its stored value does not satisfy the new installation-ID validator, replace it atomically with a newly generated valid ID.

Update storage-layout and upgrade documentation to state that this release breaks local state compatibility.

## Error Handling

Project resolution returns contextual errors for:

- empty input;
- absolute-path resolution failure;
- nonexistent paths;
- symlink-resolution failure;
- Git detection or main-checkout resolution failure;
- impossible renderer invariants.

Callers must surface these errors rather than silently selecting another identity input.

Generated-ID constructors return or panic according to their explicit API contract. No constructor returns an empty, partial, or weakly generated ID.

Readers treat legacy or invalid local project/session artifacts as unsupported input. Listing code skips them; direct requests return a not-found or invalid-local-ID error appropriate to that API. Readers never delete them.

## Testing Strategy

Read and follow `docs/testing.md`. All default tests remain deterministic and offline.

### Shared Module Tests

Add table, property, and fuzz tests for project resolution:

- relative and absolute paths;
- dot segments and trailing separators;
- nested directories inside a repository;
- symlinked paths and symlinked repository roots;
- main checkouts and multiple linked worktrees;
- submodules as distinct repositories;
- non-Git directories;
- nonexistent and unreadable paths;
- Unicode, spaces, punctuation-only components, and long paths;
- outputs that obey the safe alphabet and 80-character cap;
- distinct canonical paths with identical sanitized tails;
- stable suffix vectors and left-truncation behavior.

Test the local resolver and execution-environment adapter against the same contract fixture table. A main checkout and each linked worktree must return the same `Project` value.

Add fixed-vector, property, and fuzz tests for the generated-ID codec:

- zero, one, and maximum 128-bit values;
- leading-zero padding;
- byte-order round trips;
- bad lengths and alphabet characters;
- 128-bit overflow;
- UUID version and variant rejection;
- fixed-width lexical ordering;
- uniqueness and exact domain-prefix shapes without asserting random values.

### Consumer Tests

Cover:

- runtime state paths;
- launch configuration and trust paths;
- managed-worktree storage and resume;
- hub project grouping, archive keys, delete routes, and spawn prefills;
- fresh and forked session IDs;
- transcript, metadata, API-log, session-log, and job-store paths;
- every named generated-ID domain;
- replacement of a legacy installation ID;
- external provider/thread IDs remaining opaque.

### Clean-Break Tests

Seed old 16-hex project buckets and 26-character ULID artifacts. Prove that Serf:

- does not list or resume them as current local state;
- does not reuse their launch, trust, or managed-worktree data;
- leaves every seeded file untouched;
- writes new state only under new identifiers.

### Repository Audits

Add or extend audits that fail when production code contains:

- direct `ulid.Make` or `ulid.New` calls for Serf-owned IDs;
- project-path hashing outside the `identifier` module;
- replacement `ProjectID` or `ProjectSlug` implementations elsewhere;
- obsolete ULID dependencies after all production and test uses are removed.

Run focused module tests during implementation, then repository lint and the full deterministic test suite.

## Acceptance Criteria

1. One shared module owns project identity and every Serf-owned generated-ID payload.
2. Its project API performs absolute-path, cleaning, symlink, and Git main-checkout resolution.
3. A Git main checkout and all linked worktrees return the same project ID.
4. Project IDs contain only ASCII letters, digits, and hyphens and never exceed 80 characters.
5. Project IDs retain the most specific path tail and end in a 10-character base62 SHA-256-derived suffix.
6. Every Serf-owned ULID-backed ID uses a fixed-width, 22-character base62 UUIDv7 payload.
7. Existing domain prefixes remain unchanged.
8. Old project hashes and ULIDs receive no compatibility or migration path and remain untouched on disk.
9. External identifiers remain opaque.
10. Deterministic tests and repository audits enforce the formats and single-codepath requirement.
