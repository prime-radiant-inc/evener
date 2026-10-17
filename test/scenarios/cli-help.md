# cli-help: --help on the evener CLI subcommands prints proper usage

**What this covers**: kata `hsmm`, commit `07fdd02`. Before the fix, a
subcommand's `--help` printed only `flag: help requested` (or equivalent) and
exited 2. The fix plumbs `os.Stderr` into the FlagSet output, supplies a
structured `fs.Usage`, and detects `flag.ErrHelp` in main to exit 0.

## Pre-state

- Repo built: `go build -o ./evener ./cmd/evener/`
- Binary `./evener` exists in the repo root.

## Steps

1. `./evener openai login --help` and capture output + exit code.
2. `./evener openai logout --help` and `./evener openai status --help` —
   these route through the same dispatcher arm in `cmd/evener/main.go`.
3. `./evener launch-check --help` — another subcommand using the same
   dispatcher.
4. As a negative case, run `./evener openai bogus-subcommand`. Confirm it
   exits non-zero and prints the error plus the usage block (the error path
   was not broken by the fix).

## Expected

- Each `--help` invocation: exit 0. stdout (or stderr, depending on
  command) contains the literal `Usage:` line, all flags described,
  and at least one description sentence above the flag block.
- For `evener openai login`: usage describes both `--device` and
  `--no-device` and includes the auto-detection rules paragraph
  (mentioning `SSH_CONNECTION`, `DISPLAY`, `WAYLAND_DISPLAY`,
  `EVENER_LOGIN_HEADLESS`).
- Negative case: `./evener openai bogus-subcommand` exits non-zero and prints
  the error plus the usage block.
- Falsification: any of these prints `flag: help requested` and exits
  non-zero, or omits the usage body.

## Cleanup

- None. Read-only.

## Sharp edges

- The negative case validates that the SetOutput(os.Stderr)
  rewire didn't accidentally suppress real errors.
- `./evener` (no args) prints top-level usage with subcommand list; not
  in scope here but worth a sanity glance.
