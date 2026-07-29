# Provider-Local Plugin Agent Models — Design

Date: 2026-07-29
Status: Approved for implementation planning
Branch: `webui-workspace-shell`

## Summary

Plugin agents may declare a `model` in their frontmatter. Those values are
written for the plugin's native host: Claude Code plugins commonly use aliases
such as `sonnet`, `opus`, and `haiku`, while other plugins may name an exact
Claude or Codex model. They are not universally valid model IDs.

Serf currently applies a plugin agent's non-`inherit` model directly to the
parent profile. A Kimi session that delegates to an agent declaring
`model: sonnet` therefore constructs a Kimi profile whose wire model is
literally `sonnet`. The provider rejects the resulting request; in the
observed case the incompatible model also selected thinking behavior that
conflicted with Serf's required tool choice.

This design makes plugin agent models provider-local, advisory preferences.
Serf resolves a known catalog alias to a concrete model, verifies that the
active provider instance advertises the concrete model, and uses it only when
that verification succeeds. Otherwise Serf treats the plugin model as
unspecified and falls through to the delegate call's explicit `model`
argument, then to the parent model.

The resolution order is:

```text
available plugin model
    -> explicit delegate model
        -> parent model
```

Plugin metadata never switches providers. An explicit delegate model retains
the normal model-ref semantics, including an intentional cross-provider
selection.

## Goals

- Keep portable plugins usable when their agent definitions name models that
  the active provider does not serve.
- Preserve the existing precedence in which a usable plugin model wins over
  the delegate call's explicit model.
- Treat an unavailable plugin model as effectively unspecified, making the
  explicit delegate model its fallback.
- Resolve host aliases through the existing model catalog rather than sending
  host-runtime aliases as wire model IDs.
- Verify plugin model availability against the active provider instance before
  creating any child or durable delegate state.
- Apply one selection contract to every plugin-agent launch path, including
  direct subagent spawns and durable delegates.
- Surface the fallback without failing the delegation.
- Persist and report the concrete model that the child actually runs.

## Non-goals

- Automatically switching providers because plugin metadata names another
  provider.
- Guessing unknown model aliases or model families from string prefixes.
- Inventing a generic "latest model" ranking outside the model catalog.
- Changing the precedence of explicit delegate models when no plugin model is
  usable.
- Changing plugin command `model` behavior. Plugin command overrides are
  parsed but intentionally unenforced and already produce a load-time warning.
- Changing normal user-driven model switching or fallback routing.
- Adding backward-compatibility modes that retain the current blind plugin
  override.

## Current behavior and root cause

`plugin.Agent.Model` stores the frontmatter string and defaults to `inherit`.
During `prepareSubagentRun`, Serf first resolves the delegate call's `model`
argument and then replaces it with the plugin agent model when the latter is
non-empty and not `inherit`.

The replacement uses `resolveProfileForRef`. A bare value such as `sonnet`
does not denote a cross-provider switch, so the resolver calls
`base.WithModel("sonnet")`. On a Kimi profile this preserves the Kimi provider
and changes only the wire model. No availability check occurs in this path.

This is a semantic mismatch:

- Claude Code's `sonnet`, `opus`, and `haiku` values are aliases resolved by
  the Claude Code runtime.
- Serf currently treats the same values as provider wire model IDs.
- Speaking the Anthropic wire protocol does not make a provider an Anthropic
  model host. Kimi's behavior tag and advertised model set remain Kimi's.

The failure is therefore rooted in model selection, not tool-choice encoding.
Changing required tool choice globally would mask this selection error and
would regress models that support required tool choice.

## Existing seams to reuse

Serf already has the two mechanisms this feature needs:

1. `llm.ModelInfo.Aliases` and `ModelCatalog.GetModelInfo` map an alias to its
   canonical `ModelInfo`. Real model IDs take precedence over aliases.
2. `llm.Client.ListModels` obtains the models advertised by a provider
   instance. `Session.SetModel` already uses one bounded enumeration for live
   metadata and membership validation.

The embedded catalog's production data currently declares no aliases. The
alias mechanism is implemented and tested, but this change must add only the
aliases Serf intentionally supports. Initially:

- `sonnet`
- `opus`
- `haiku`

No Codex alias is added without an observed or documented source-runtime
alias. Exact Codex model IDs continue through exact-ID validation.

These catalog aliases are maintained like the catalog's other model metadata:
when the intended concrete target changes, the catalog update changes the
alias target. Provider discovery still decides whether that target is
available to the configured instance.

## Model-source semantics

There are three possible model sources:

| Source | Meaning | Failure policy |
|---|---|---|
| Plugin agent `model` | Provider-local preference supplied by third-party metadata | Fall through with a warning |
| Delegate call `model` | Explicit fallback supplied for this invocation | Use the normal resolver and its existing error policy |
| Parent profile | Final inherited default | Always available because the parent is already running |

A plugin model is not a cross-provider request, even when it contains a
provider-qualified ref. A plugin ref that would select another provider is
simply unavailable in the current provider and falls through.

An explicit delegate model is evaluated only after the plugin preference is
absent or unavailable. Its existing cross-provider behavior is preserved.

## Resolution algorithm

Model selection runs once, before delegate IDs, worktrees, job records, child
sessions, transcripts, or watches are created.

### 1. Load the plugin agent

If `agent_type` is present, resolve it from the effective plugin-agent
catalog. Unknown agent types remain errors.

Normalize the plugin model by trimming whitespace:

- empty or `inherit`: no plugin preference;
- any other value: candidate plugin preference.

### 2. Reject automatic provider switching

Evaluate the plugin candidate against the current profile only. Do not invoke
the session's cross-provider resolver for plugin metadata.

If the candidate is a ref that the current profile classifies as a
cross-provider switch, mark the plugin preference unavailable and continue to
fallback selection.

Meta-providers retain their existing namespace semantics. For example, an
OpenRouter model ID containing `anthropic/...` is not a provider switch when
the current OpenRouter profile treats that prefix as part of its wire model.
It must still pass live membership validation.

### 3. Canonicalize a known alias

Look up the candidate in the embedded model catalog:

- If the lookup returns a `ModelInfo` whose `ID` differs from the candidate,
  the candidate is an alias. Its canonical model is that `ID`.
- If the candidate is already a real model ID, keep it unchanged.
- If neither a real ID nor an alias is known, keep the exact candidate for
  live membership matching. An advertised custom model ID is valid even when
  absent from the embedded catalog; do not guess its vendor or family.

Catalog lookup translates names but never proves that the active provider
serves the result.

### 4. Verify active-provider membership

Fetch the current provider instance's model list once with a bounded context.
The live list is authoritative for this advisory selection.

A plugin preference is usable when:

- the advertised wire ID exactly matches the requested exact model; or
- for a catalog alias, an advertised wire ID exactly matches the alias's
  canonical model after applying only the current profile's established
  self-prefix/meta-provider normalization.

Use the existing normalized live-ID comparison rules for surrounding
whitespace and case. Do not use catalog family fallback to satisfy membership.

Do not collapse an exact dated model into an undated family. An exact plugin
pin is usable only when that exact model is advertised.

When the plugin alias resolves to a concrete model, the child profile uses the
provider's advertised wire ID, not the original alias.

If the plugin candidate already equals the parent profile's current concrete
model, Serf may use the parent profile without another enumeration.

The plugin preference is unavailable when:

- the current provider advertises models and no valid match exists;
- model enumeration fails or is unsupported;
- the candidate would switch providers; or
- alias resolution is ambiguous.

Because the plugin value is advisory, inability to verify availability falls
through rather than gambling on a provider request.

### 5. Select the fallback

If the plugin preference is unavailable:

1. Resolve the delegate call's explicit `model` through the existing normal
   resolver, if supplied.
2. Otherwise inherit the parent profile.

If the plugin preference is absent (`inherit` or empty), use the same fallback
sequence without emitting an unavailable-model warning.

If the plugin preference is verified, it wins and the explicit delegate model
is not evaluated.

### 6. Freeze the result

Child construction receives the selected concrete profile rather than
rerunning model selection.

Persist the selected provider ID and concrete model in the delegate restore
descriptor. Restore validates and reconstructs that frozen model; it does not
re-read current plugin metadata or retry a previously rejected plugin model.

`RequestedModel` records the model source that actually won selection:

- the original plugin alias or exact ref when the plugin preference wins;
- the explicit delegate ref when the plugin preference falls through; or
- empty when the parent model is inherited.

`ResolvedProfileID` and `ResolvedModel` always record the concrete frozen
profile. This preserves the distinction between a requested alias and the wire
model selected from it without persisting a rejected plugin preference as the
child's requested model.

The delegate result continues to echo the concrete
`provider/model actually resolved` value.

## Diagnostics

Falling through from a non-`inherit` plugin model emits a non-fatal
`EventWarning` for that delegation. The warning contains:

- plugin name and agent type;
- requested plugin model;
- active provider instance;
- reason: cross-provider, unavailable, unverified, or ambiguous;
- selected explicit fallback or inherited parent model.

Example:

```text
plugin "superpowers-chrome" agent "browser-user" requested model "sonnet",
which is unavailable from provider "kimi-anthropic-api"; using
"kimi-anthropic-api/k3"
```

The warning must not contain credentials, endpoint URLs, or model-list
response bodies. It is diagnostic only and does not trigger plugin
notification hooks. Use the existing `emitDiagnosticWarning` path rather than
the model-facing `emit(EventWarning, ...)` path.

Emit the warning after a concrete fallback profile has been selected but
before child or durable delegate state is created. If the explicit fallback
itself cannot be resolved, return that error without claiming that a fallback
model was selected.

## Data flow

```text
delegate(model=fallback, agent_type=plugin:agent)
    |
    v
load plugin agent model
    |
    +-- empty/inherit ------------------------------+
    |                                               |
    v                                               |
catalog alias -> concrete candidate                 |
    |                                               |
current-provider-only membership check              |
    |                                               |
    +-- available --> freeze plugin profile         |
    |                                               |
    +-- unavailable --> warning --------------------+
                                                    |
                                                    v
                                      explicit delegate model?
                                         | yes       | no
                                         v           v
                                  normal resolver   parent profile
                                         \           /
                                          v         v
                                      freeze concrete profile
                                              |
                                              v
                                      construct child and job
```

## Error handling

- Unknown `agent_type`: return the existing error.
- Plugin provider switch: warn and fall through.
- Plugin model absent from an enumerable provider: warn and fall through.
- Plugin availability cannot be verified: warn and fall through.
- Explicit delegate fallback cannot be resolved: return the existing model
  resolution error; do not silently inherit.
- Parent model: inherit without revalidation.
- Failure after the profile is frozen follows existing delegate construction
  and rollback behavior.

The plugin availability check must complete before any durable or externally
visible delegate state is created, so a fallback decision cannot leave orphan
IDs, worktrees, jobs, or transcripts.

## Testing

Before changing tests, implementation must continue to follow
`docs/testing.md`. Tests use scripted/fake providers and exercise the real
selection and delegate construction path; default tests never call a live
provider.

Required deterministic cases:

1. Kimi parent + plugin `sonnet` + explicit `k3` fallback selects Kimi `k3`
   and never sends a request with model `sonnet`.
2. Kimi parent + plugin `sonnet` + no explicit fallback inherits the parent's
   concrete model.
3. Available plugin exact model wins over an explicit delegate fallback.
4. Available catalog alias canonicalizes to the concrete advertised model and
   wins.
5. Plugin `inherit` uses the explicit delegate model without warning.
6. Plugin `inherit` with no explicit model inherits the parent.
7. A provider-qualified plugin model never switches providers, even when the
   target provider is configured.
8. A meta-provider namespaced model is allowed only when its current instance
   advertises the corresponding wire ID.
9. Successful enumeration without the requested model falls through and
   warns.
10. Enumeration failure or unsupported enumeration falls through and warns.
11. An invalid explicit fallback still returns an error after the plugin model
    falls through.
12. Persisted restore fields and delegate result echo the concrete selected
    fallback, not the rejected plugin model.
13. Selection failure or fallback occurs before delegate IDs, worktrees, job
    records, child transcripts, and watches are created.
14. Exact dated model IDs are not satisfied by a different snapshot from the
    same family.
15. Alias entries do not shadow concrete catalog model IDs.
16. Direct plugin-agent spawn and durable delegation produce the same selected
    concrete profile for the same parent, plugin model, and explicit fallback.
17. An unknown catalog ID that is advertised exactly by the active provider is
    accepted as a custom model rather than misclassified as an unknown alias.

Assertions should inspect structured profiles, requests, warnings, and restore
descriptors. They must not primarily regex-match rendered scripts or large
serialized payloads.

## Documentation changes

- Update plugin agent documentation to state that `model` is a
  provider-local preference under Serf.
- Document the resolution order:
  `available plugin model -> explicit delegate model -> parent model`.
- Document that plugin metadata never triggers a provider switch.
- Document the supported catalog aliases and that unadvertised values fall
  through.
- Keep the plugin command override warning documentation unchanged.

## Alternatives considered

### Provider-family string heuristics

Classify `claude-*`, `gpt-*`, `sonnet`, and similar strings by prefix and
compare them with the current provider behavior tag.

Rejected because it cannot determine whether an exact model is retired or
disabled for the configured account, misclassifies custom deployments, and
duplicates knowledge already represented by catalog aliases and provider
model discovery.

### Strict plugin pins

Reject delegation when the plugin model cannot be verified.

Rejected because third-party plugin metadata should not make an otherwise
portable agent unusable. Jesse selected fallback semantics.

### Ignore every plugin model

Always use the explicit delegate model or parent.

Rejected because it discards useful plugin specialization when the requested
model is genuinely available on the active provider.

### Catalog-only validation

Use catalog provider metadata without consulting the configured provider.

Rejected because the embedded catalog describes known models, not account-,
gateway-, or instance-specific availability.

## Implementation boundaries

The implementation should remain narrow:

- centralize subagent model selection in one side-effect-free preflight;
- route direct spawn and durable delegate creation through that same preflight;
- reuse `ModelCatalog` alias lookup and `llm.Client.ListModels`;
- avoid a second availability query during child construction;
- do not add provider-family routing tables;
- do not change request-builder tool-choice behavior;
- do not modify unrelated plugin command handling.
