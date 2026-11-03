# Upstream provenance and local ownership

## Immutable identities and licenses

This package derives from Santhosh Tekuri's
`github.com/santhosh-tekuri/jsonschema/v5@v5.3.1`, source repository
https://github.com/santhosh-tekuri/jsonschema, tag `v5.3.1`, commit
`16bce71af51f6a4a775f11e649a347a8803940d3`.
The retained [LICENSE](LICENSE) is Apache-2.0, unchanged.
The Go module checksums of the imported release were:

```text
github.com/santhosh-tekuri/jsonschema/v5 v5.3.1 h1:lZUw3E0/J3roVtGQ+SCrUrg3ON6NgVqpn3+iol9aGu4=
github.com/santhosh-tekuri/jsonschema/v5 v5.3.1/go.mod h1:uToXkOrWAZ6/Oc07xWQrPOhJotwFIyu2bBVN41fcDUY=
```

The source commit's `testdata/JSON-Schema-Test-Suite` gitlink pins
`8cdfac41e37527795879e480a483997cbd6188f3` from
https://github.com/json-schema-org/JSON-Schema-Test-Suite.
Its complete `tests/`, `remotes/` and
[LICENSE](testdata/JSON-Schema-Test-Suite/LICENSE) are committed unchanged.
That MIT-style license retains `Copyright (c) 2012 Julian Berman`.
`tests/latest` remains the upstream symlink to `draft2020-12`.

During import, the cached source files were compared byte-for-byte with the
immutable source commit, its corpus gitlink was checked, and the copied corpus
was compared byte-for-byte and by exact relative-path inventory with that
immutable corpus checkout. No upstream `go.mod`, Git metadata, CI configuration
or repository scaffolding is included.

## Corpus inventory

`TestFixtureCorpus` decodes the committed files and counts declared groups and
cases, including optional cases. It does not validate the conformance corpus.
The imported supported-draft inventory is:

| Draft directory | JSON files | Groups | Declared cases |
| --- | ---: | ---: | ---: |
| draft4 | 43 | 191 | 832 |
| draft6 | 52 | 265 | 1093 |
| draft7 | 63 | 300 | 1396 |
| draft2019-09 | 76 | 415 | 1793 |
| draft2020-12 | 77 | 420 | 1824 |
| **Supported total** | **311** | **1591** | **6938** |

The complete upstream `tests/` tree has 425 physical JSON files plus the
`latest` symlink. It also retains `draft3` (39 files, 125 groups, 544 cases)
and `draft-next` (75 files, 419 groups, 1867 cases), without claiming support
for either. The `remotes/` tree has 73 JSON reference fixtures. Including the
corpus license and symlink, 500 corpus paths are imported. The source release's
own `testdata/` contributes another 21 unchanged fixture files.

## Retained baseline exclusions

`schema_test.go` retains the v5.3.1 `skipTests` map exactly, with no added skips
or changed validity expectations. Its exact map text, from immediately after
`var skipTests = ` through the newline before `func TestDraft4`, has SHA-256
`b65f01532208804e73ef58ac76725e6a64832f62b7e615b9aee495679eb19e56`.

The inherited exclusions are the draft4 zero-terminated-float group,
ECMAScript/Go-regexp differences in each supported draft, and optional
`idn-hostname`/`idn-email` formats in draft7, draft2019-09 and draft2020-12.
The precise group and case exclusions and upstream explanations remain in the
map. The inherited harness and its extra-fixture directory selections remain
otherwise unchanged. Metaschema validation stays enabled.

## Local modifications in this checkpoint

Modified inherited files carry notices identifying Evener's changes:

- `doc.go`: local optional-loader usage path and modification notice.
- `schema.go`: constant `%s` format when rendering the precomputed enum error,
  required by Go 1.27's default vet check. Percent signs in enum values are
  rendered literally rather than reinterpreted as formatting directives.
  The five public numeric constraint fields, integer recognition, ordered bounds
  and divisibility use the immutable compact decimal kernel. Numeric diagnostics
  use compact canonical encoding rather than Float64 conversion.
  Examined nonfinite Go floats and malformed programmatic `json.Number` values
  (including `01` and `1/2`) return `InvalidJSONTypeError`, rather than the old
  number-only acceptance or arithmetic panic. Valid finite Go values retain the
  existing kind switch and `fmt.Sprint` interpretation.
  Equality and hashing move to `identity.go`; `uniqueItems` calls its shared
  duplicate helper with a call-local hasher and retains the same error order.
- `identity.go`: derived equality, hashing and duplicate traversal from
  `schema.go`. Numeric identity uses the same admitted converter and immutable
  decimal kernel, without rational conversion. Hashes frame type tags, string
  and container lengths, sorted object keys and canonical numeric encodings.
  Pairwise comparison remains in use through length 20; larger arrays use hash
  buckets, with exact equality resolving every collision and the original
  earliest-second-then-first duplicate order preserved. Invalid numeric leaves
  examined by hashing return `InvalidJSONTypeError` too. No eager instance
  admission pass is added.
- `compiler.go`: compile all five numeric bounds through the compact decimal
  loader, including built-in metaschema bootstrap. Preserve draft-specific
  exclusive-bound dispatch and reject nonpositive divisors.
- `httploader/httploader.go`: local jsonschema import and usage path, notice.
  The loader's actual HTTP behavior is unchanged.
- `schema_test.go`: local imports, per-compiler `LoadURL` routing of the
  upstream localhost URL identities to committed fixture files, refusal of
  unexpected external fixture URLs, removal of fixed-port `TestMain` servers,
  and ephemeral HTTP/TLS servers exercising the actual optional loader in
  `TestCompileURL` and `TestInvalidSchema`. A private certificate-trusting test
  client rejects destinations outside those servers and restores the original
  loader client. No global TLS verification bypass remains.
- `extension_test.go`: local import and typed validation-error/cause/location
  assertions instead of the inherited diagnostic-message substring assertion.
- `example_extension_test.go`: local import, retained successful validation,
  and an additional rejection example whose output is the concrete error type
  and keyword location, not diagnostic prose.
- `example_test.go`: local import and modification notice. Existing successful
  examples retain their empty expected outputs.
- `README.md`: ownership/checkpoint documentation and local loader usage path.

`content.go`, `draft.go`, `errors.go`, `extension.go`, `format.go`,
`loader.go`, `output.go` and `resource.go` are byte-identical to the
pinned source. `format_test.go`, `internal_test.go`, both licenses and all
fixtures are unchanged. New Evener files are `UPSTREAM.md`, `corpus_test.go`
and `draft_baseline_test.go`. Compact numeric additions are `numeric.go`,
`numeric_test.go`, `numeric_metaschema_test.go`, and the `decimal/` kernel and
independent oracle tests. Shared test-only resource containment lives in
`../internal/numerictest/`.
`identity_test.go` adds exact nested const/enum/uniqueItems regressions, framed
hash tests, contained unsafe witnesses and injected-collision/call-count tests
of the production duplicate helper, without global mutable test hooks.

The llm callers in `generate.go`, `generate_object.go` and
`generate_edges_fuzz_test.go` change only their jsonschema import. The llm
module manifests remove the old dependency. Compiler, schema and error types
now belong to this package; this is not an alias or a second validation path.

## Maintenance and delivery boundary

The llm maintainers own security triage, upstream advisory monitoring, license
preservation, corpus updates and reconciliation of upstream fixes. An upstream
update must record its immutable source/corpus revisions here, review local
deltas and retained exclusions, and pass CI's full imported conformance harness.
Tests never fetch fixtures by default. The optional HTTP loader remains opt-in
for applications; the inherited test suite imports and exercises it locally.

This checkpoint bounds parsing, compilation, direct numeric predicates and
numeric equality/hash in encoded mantissa/exponent size. It does **not**
establish wholly bounded validation: cardinality fields still use native
integers. Their migration is a later task, not a compatibility fallback in the
new decimal API. Hash collisions can still require pairwise exact comparisons.
No decoder or Go-kind switch is widened.
The validator and decimal kernel remain leaf packages inside the existing llm
module, with no nested module and no imports of parent llm, agent or application
packages.
