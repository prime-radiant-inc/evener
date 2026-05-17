# cli-help: --help on serf-tui and serf openai prints proper usage

**What this covers**: kata `hsmm`, commit `07fdd02`. Before the fix, both
`./serf-tui --help` and `./serf openai login --help` printed only
`serf-tui: flag: help requested` (or equivalent) and exited 2. The fix
plumbs `os.Stderr` into the FlagSet output, supplies a structured
`fs.Usage`, and detects `flag.ErrHelp` in main to exit 0.

## Pre-state

- Repo built: `go build -o serf ./cmd/serf && go build -o serf-tui ./cmd/serf-tui`
- Binaries `./serf` and `./serf-tui` exist in the repo root.

## Steps

1. `./serf-tui --help` and capture output + exit code.
2. `./serf openai login --help` and capture output + exit code.
3. `./serf openai logout --help` and `./serf openai status --help` —
   these route through the same dispatcher arm in `cmd/serf/main.go`.
4. `./serf launch-check --help` — another subcommand using the same
   dispatcher.
5. As a negative case, run `./serf-tui --bogus`. Confirm it still
   exits 2 with `flag provided but not defined: -bogus` AND the full
   usage block (the error path was not broken by the fix).

## Expected

- Each `--help` invocation: exit 0. stdout (or stderr, depending on
  command) contains the literal `Usage:` line, all flags described,
  and at least one description sentence above the flag block.
- For `serf-tui`: usage includes `Environment variables:` listing
  `SERF_HUB_ADDR`, `SERF_HUB_BIN`, `SERF_STATE_DIR`, `SERF_TUI_LOG_FILE`,
  `SERF_HUB_AUTH_TOKEN`.
- For `serf openai login`: usage describes both `--device` and
  `--no-device` and includes the auto-detection rules paragraph
  (mentioning `SSH_CONNECTION`, `DISPLAY`, `WAYLAND_DISPLAY`,
  `SERF_LOGIN_HEADLESS`).
- Negative case: `./serf-tui --bogus` exits 2 and prints
  `flag provided but not defined` plus the usage block.
- Falsification: any of these prints `flag: help requested` and exits
  non-zero, or omits the usage body.

## Cleanup

- None. Read-only.

## Sharp edges

- The non-flag negative case validates that the SetOutput(os.Stderr)
  rewire didn't accidentally suppress real errors.
- `./serf` (no args) prints top-level usage with subcommand list; not
  in scope here but worth a sanity glance.
