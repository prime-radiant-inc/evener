# Maintained JSON Schema validator

`primeradiant.com/evener/llm/jsonschema` is Evener's maintained validator,
derived from Santhosh Tekuri's Apache-2.0 `jsonschema/v5@v5.3.1`.
The llm maintainers own releases, security triage, upstream reconciliation and
corpus updates. [UPSTREAM.md](UPSTREAM.md) records the immutable source and
corpus revisions, licenses, local changes and inherited exclusions.

`Generate` tool validation, `GenerateObject` and `StreamGenerateObject` use this
backend. Agent still uses the upstream validator until the separate agent cut.
This package and [`decimal`](decimal) are public leaf packages inside the llm
module: no nested `go.mod`, and no imports of parent llm, agent or applications.
Their release unit is the llm module, not an independently versioned fork.

## Compiler and errors

Use `NewCompiler`, `AddResource` and `Compile`, or `CompileString`, then
`Schema.Validate` on an already decoded value. Drafts 4, 6, 7, 2019-09 and
2020-12 retain their draft-specific keyword behavior. `Compiler.Draft` selects
the default when `$schema` is absent, currently 2020-12.
Built-in draft metaschemas validate schemas during compilation. A declared
custom `$schema` is resolved for dialect/vocabulary handling but does not alone
apply that document's arbitrary assertions. Custom keyword metaschema assertions
run through `Compiler.RegisterExtension`, before the extension compiler.
The optional HTTP loader requires an explicit blank import of
`primeradiant.com/evener/llm/jsonschema/httploader`; the base package does not
implicitly enable HTTP loading.

Compilation errors wrap in `*SchemaError`. Validation returns
`*ValidationError` with ordered `Causes`, `InstanceLocation`, `KeywordLocation`
and `AbsoluteKeywordLocation`. Existing error traversal and flag/basic/detailed
output APIs remain. Numeric and cardinality diagnostics render exact compact
canonical JSON numbers, not approximate floats or exponent-expanded digits.
Consumers should inspect error types and locations, not diagnostic wording.

## Exact numeric API

The five `Schema` numeric fields are `*decimal.Number`:

- `Minimum`, `ExclusiveMinimum`, `Maximum`, `ExclusiveMaximum`, `MultipleOf`.

The eight cardinality fields have the same type:

- `MinProperties`, `MaxProperties`, `MinItems`, `MaxItems`.
- `MinLength`, `MaxLength`, `MinContains`, `MaxContains`.

Every nil means absent, including cardinalities: no negative sentinel or
approximate compatibility field exists. Explicit zero differs from absence.
When `contains` is active, absent `MinContains` has an effective minimum of one;
draft and vocabulary activation rules still apply. String counts are Unicode
code points. Schema counts remain exact even beyond native integer range; only
actual container, string and match counts use native integers. Compilation
rejects negative/nonintegral cardinalities and nonpositive `multipleOf`.

`decimal.Number` is immutable and its zero value is zero. `decimal.Parse`
accepts JSON number syntax. `Sign`, `IsInteger`, `Cmp`, `Equal` and `MultipleOf`
operate exactly; `String` emits compact canonical syntax without implied zero
digits. `MultipleOf` returns false for a nonpositive divisor. There is no general
rational conversion, mutable limb API, interning or arithmetic fallback.
Compiled values can be reused concurrently when callers do not mutate the
schema. Arithmetic temporaries and identity hashers are call-local.

Numeric equality also governs nested `const`, `enum` and `uniqueItems`.
Uniqueness keeps pairwise comparison through 20 items and hash buckets above
20; exact equality resolves collisions. This does not promise linear time for
colliding hashes or bound general schema recursion and instance traversal.

## Admission and resource boundary

Parsing, numeric compilation, predicates and numeric identity work/storage
follow encoded mantissa and exponent sizes, not the magnitude of an implied
power of ten. No exponent is narrowed to a native integer, no `10^E` is
materialized and no implied zero digits are allocated. This is an arithmetic
resource guarantee, not a total validator CPU/memory limit or an input-size
limit. Code review is the primary exponent-magnitude proof.

Decoding remains the caller's responsibility. Schema resources and llm object
generation retain `UseNumber`; llm tool handlers retain ordinary `Unmarshal`
and receive float64 numbers, including its rounding and underflow behavior.
Overflow remains rejected there. Validation cannot recover discarded token
precision. Valid finite programmatic values retain the admitted Go-kind switch
and `fmt.Sprint` interpretation. When examined as numbers, NaN, infinity and
malformed `json.Number` values, including `01` and `1/2`, return
`InvalidJSONTypeError`. There is no eager whole-tree admission pass.

## Verification and distribution

Fixtures are committed and tests do not download the corpus. CI owns full
conformance, existing llm regressions, fuzz seeds, race and tagged-consumer
gates. Focused evidence checks are:

```sh
cd llm
go test . -run '^TestGenerate(NumericValidationJourney|ObjectNumericValidationJourney)$' -count=1
go test ./jsonschema -run '^Test(NumericResourceBound|CompiledNumericConcurrentValidation|LeafPackagesOutsideWorkspace)$' -count=1
```

`TestNumericResourceBound` includes compilation and validation in the existing
subprocess helper's measured callback, with `GOMAXPROCS=1`, allocation/CPU
metrics and Linux hard-limit readback. Its regression budgets are not production
admission limits or a universal complexity bound. Non-Linux containment differs.
`TestLeafPackagesOutsideWorkspace` builds and runs only the two leaf packages
through a temporary consumer with `GOWORK=off GOPROXY=off GOSUMDB=off` and only a
local llm replacement. This does not establish publication readiness for the
whole llm module, whose other packages still depend on workspace sibling modules.
