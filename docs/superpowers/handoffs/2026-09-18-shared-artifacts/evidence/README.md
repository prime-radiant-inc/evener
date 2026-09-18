# Qualification evidence snapshot

These are observations from the September 18, 2026 G0 dependency experiments
and baseline tests at `0a53ef91f517c8d323a2381a493233b51ab59e22`. They do not
qualify the later production implementation or constitute A01–A22 acceptance.

- `resolved-go-modules.json`: original workspace module resolution converted
  from concatenated JSON objects to an array, with local cache paths removed.
  Root-module isolation at that baseline still declared MCP v1.3.0; the contracts
  implementation subsequently aligned it to v1.6.1.
- `dependency-pins.json`: exact scratch npm dependency versions and integrity.
- `codec-results.json` and `default-handler-results.json`: actual pinned SDK
  codec behavior and the built-in display-mode handler result.
- `browser-results.json`: original Chrome experiment output, including denied
  requests, CSP observations and the explicit document-replacement barrier.
  The only cookie value is a synthetic non-secret fixture. Loopback ports are
  ephemeral observations, not production configuration.
- `baseline-test.log`: original compact `make test` runner output. Timings are
  the runner's reported values, not end-to-end performance measurements.

Browser: Google Chrome 153.0.8010.36, headless, on macOS 26.5.2 (25F84),
Darwin 25.5.0 arm64. The scratch launch explicitly mapped
`artifact-sandbox.localhost` to loopback; this does not prove default OS DNS
provisioning. No Safari, Firefox, remote HTTPS or live-provider run is included.

The scratch harness sources, install directory and full audit reports remain
local and are not included in this repository. These committed snapshots make
the observations reviewable; they are not a standalone reproduction kit.
Production tests and assets must provide the repeatable release qualification.
The command inventory in the parent contract describes the local experiment,
not commands that work from this evidence directory.
