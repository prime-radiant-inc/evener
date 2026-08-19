# cli-help: --help on evener-tui and evener openai prints proper usage

**What this covers**: kata `hsmm`, commit `07fdd02`. Before the fix, both
`./evener-tui --help` and `./evener openai login --help` printed only
`evener-tui: flag: help requested` (or equivalent) and exited 2. The fix
plumbs `os.Stderr` into the FlagSet output, supplies a structured
`fs.Usage`, and detects `flag.ErrHelp` in main to exit 0.

## Pre-state

- Repo built: `go build -o evener ./cmd/evener && go build -o evener-tui ./cmd/evener-tui`
- Binaries `./evener` and `./evener-tui` exist in the repo root.

## Steps

1. `./evener-tui --help` and capture output + exit code.
2. `./evener openai login --help` and capture output + exit code.
3. `./evener openai logout --help` and `./evener openai status --help` —
   these route through the same dispatcher arm in `cmd/evener/main.go`.
4. `./evener launch-check --help` — another subcommand using the same
   dispatcher.
5. As a negative case, run `./evener-tui --bogus`. Confirm it still
   exits 2 with `flag provided but not defined: -bogus` AND the full
   usage block (the error path was not broken by the fix).

## Expected

- Each `--help` invocation: exit 0. stdout (or stderr, depending on
  command) contains the literal `Usage:` line, all flags described,
  and at least one description sentence above the flag block.
- For `evener-tui`: usage includes `Environment variables:` listing
  `EVENER_HUB_ADDR`, `EVENER_HUB_BIN`, `EVENER_STATE_DIR`, `EVENER_TUI_LOG_FILE`,
  `EVENER_HUB_AUTH_TOKEN`.
- For `evener openai login`: usage describes both `--device` and
  `--no-device` and includes the auto-detection rules paragraph
  (mentioning `SSH_CONNECTION`, `DISPLAY`, `WAYLAND_DISPLAY`,
  `EVENER_LOGIN_HEADLESS`).
- Negative case: `./evener-tui --bogus` exits 2 and prints
  `flag provided but not defined` plus the usage block.
- Falsification: any of these prints `flag: help requested` and exits
  non-zero, or omits the usage body.

## Cleanup

- None. Read-only.

## Sharp edges

- The non-flag negative case validates that the SetOutput(os.Stderr)
  rewire didn't accidentally suppress real errors.
- `./evener` (no args) prints top-level usage with subcommand list; not
  in scope here but worth a sanity glance.
