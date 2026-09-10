# Historical installed SDK upgrade outcome

This receipt preserves a bounded SDK upgrade outcome recorded on 2026-09-09. It is historical source evidence, not a current qualification or release claim.

The run exercised the installed `@evener/appwire-client` against a real registered `evener/upgrade` AppWire handler and the real `selfupdate.Upgrade` path. A scripted release fixture first forced an upgrade failure, which the SDK reported as `uncertain`/`unverified`; an explicit retry then returned `acknowledged`. The receipt recorded two downloads and two binary installs, matching installed hashes, successful execution of the installed `evener` binary, and the expected unsupported `--version` response from `evener-dev`. A fresh installed Hub was launched under owned temporary state and read back the expected commit identity before clean shutdown.

The retained receipt records source, SDK, binary, driver, harness, and raw-evidence SHA256 values, request counts, cleanup observations, and exact limitations. The companion driver is preserved as a redacted historical recipe; private temporary paths, auth-token locations, process identifiers, and runtime credentials are omitted.

Round 1 remains explicitly excluded because first-run startup attempted an unsolicited marketplace clone. The accepted bounded run pre-created the supported empty marketplace state and disabled plugin auto-upgrade; offline mode was supplementary. No real provider account, credentials, simulator, native UI, or production prefix was used.

The result does not qualify SDK execution as independently acknowledged by the SDK itself: the SDK still labels the upgrade execution `unverified`. It also does not establish live provider behavior, release distribution, disconnect or restart behavior, or physical-device qualification. Treat the JSON receipt and hashes as historical evidence tied to their recorded source commit and installed artifact identities.
