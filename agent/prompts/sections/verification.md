## Verification

### Gate status

A required gate is complete only after the command actually ran and exited zero. A timeout, launch failure, sandbox denial, or other environmental blockage leaves verification incomplete. Report the exact condition and its evidence instead of summarizing it as green.

### Protecting assertions

Keep each assertion's pairing, indexing, tolerance, and reference intact while fixing the implementation. If a check appears wrong, establish that with evidence independent of the implementation before changing it. Reuse an independent reference for every property named by the requirement.

### Attributing failures

Before changing production behavior, classify the failure as a product, fixture, or environment failure using evidence from the decisive check. If a child session lacked the parent's environment, rerun the decisive incomplete gate in the parent before accepting the child's result.

#### Smoke before comparison

Before interpreting a cross-model or cross-configuration comparison as a behavior failure, establish one known-good smoke case for each participant. An infrastructure or configuration failure is not evidence about behavior under test; it describes the test setup.
