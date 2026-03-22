# Bug: Panic in tool_registry.go compileSchema during session init

**Date:** 2026-03-18
**Severity:** Blocking
**Provider:** openai
**Model:** gpt-5.4-mini

## Summary

Serf panics during session initialization when compiling a JSON schema for tool registration. The crash occurs in `compileSchema` → `jsonschema.Compiler.AddResource`, preventing the session from starting.

## Stack trace

```
github.com/santhosh-tekuri/jsonschema/v5.newResource({0x1024ccaca, 0xb}, {0x1026bc020, 0x1400039c8d0})
    jsonschema/v5@v5.3.1/resource.go:35
github.com/santhosh-tekuri/jsonschema/v5.(*Compiler).AddResource(...)
    jsonschema/v5@v5.3.1/compiler.go:103
primeradiant.com/serf/agent.compileSchema(0x1024cb0b8?)
    serf/agent/tool_registry.go:402
primeradiant.com/serf/agent.(*ToolRegistry).Register(...)
    serf/agent/tool_registry.go:134
primeradiant.com/serf/agent.(*baseProfile).NewToolRegistry(...)
    serf/agent/profile.go:104
primeradiant.com/serf/agent.(*Session).initSessionState(...)
    serf/agent/session.go:1968
primeradiant.com/serf/agent.NewSession(...)
    serf/agent/session.go:329
main.run(...)
    serf/cmd/serf/run.go:186
```

## Reproduction

Triggered by toil's `integrate` workflow, which invokes serf as the `integration_tester` node. The crash happens before any tool calls — during `NewSession` → `initSessionState` → `NewToolRegistry` → `Register` → `compileSchema`.

```bash
export SERF_PROVIDER=openai
export SERF_MODEL=gpt-5.4-mini

# Exact reproduction TBD — may depend on which tools/profile are active
# in the integrate workflow context
serf --provider openai --model gpt-5.4-mini --verbose \
  --dir /path/to/worktree \
  "Run integration tests"
```

## Likely cause

A tool's JSON schema is malformed or contains a construct that `jsonschema/v5` cannot handle. The `compileSchema` function at `tool_registry.go:402` passes the schema to `Compiler.AddResource`, which panics (or returns an unrecovered error) in `newResource`.

Possible sources:
- A tool definition with an invalid `$schema` or `$id` field
- A schema using a JSON Schema draft version not supported by v5.3.1
- An MCP-provided tool returning a schema that triggers a bug in the library

## Affected toil runs

- `delta-velvet-jet` → child `iris-brisk-summit` (integration_tester node)
