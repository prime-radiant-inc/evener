# Execution support

`primeradiant.com/evener/execsupport` contains the shared low-level helpers
used by Evener's root, agent, and LLM modules:

- `valueexpr` evaluates configuration value expressions and command-backed
  values.
- `procgroup` manages platform process groups.
- `orphanpipe` handles subprocess pipe-drain results, with test fixtures in
  `orphanpipe/orphanpipetest`.
- `shellquote` renders POSIX shell words.

The module depends only on the standard library and `golang.org/x/sync`. It
does not import application packages, `agent`, `llm`, `envvars`, AppWire, or
the root module. Run its standalone tests with:

```sh
GOWORK=off go test ./...
```
