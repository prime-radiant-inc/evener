Native preferences can probe and discard a corrupt local keybinding draft without a connected hub. Compare-and-swap discard preserves a readable replacement, and a matching live model reclassifies through the shared store’s existing recovery path. The production provider uses the same raw storage backend exercised by tests.

This is the provider slice after merged #1950. Retained same-hub draft projection is #1964; the offline recovery screen is #1904. Own non-test scope is 195 changed lines.

Validation: provider, native draft storage, and shared discard tests; native typecheck; package-import lint; independent correctness/simplification review; and local RoboRev completed. All 15 CI checks passed at `e4f03c86fcc9d3a8fa947af5d853000469a53dfb`.

All three current-head raw RoboRev member bodies were read. The screen-consumer finding is the concrete #1904 seam: its recovery action calls `discardUnreadableKeybindingsDraft`. The retained decoder and snapshot projection are implemented in #1964. Remaining Low findings are tracked separately in #1941 (classification uncertainty after the verification read fails) and #1948 (provider re-probe and storage-failure coverage). No accepted Low was folded into this patch.

The complete owned binary patch is identical to the previously qualified provider patch, SHA-256 `aedeb4a4f92473bff7a43b8fe401607007991bfa67c18976948ab7e4c42f4dc8`. Base is main `5afd878f3f6c9c262843485a354b018d40b78315`.
