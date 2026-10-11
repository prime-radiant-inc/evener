// Test-only extractor for Go-source constant bindings: reads one constant's
// value out of `?raw`-imported Go source, so a client test pins its
// discriminant to the daemon's own constant and a Go-side rename or value
// change fails the test instead of silently leaving the client matching a
// discriminator the hub no longer sends. Shared by the errors and warnings
// suites (mobile's conversation suite still binds its decoder literals with
// its own readFileSync guard and can adopt this when next touched);
// test-support only - the
// @evener/appwire-client/testing subpath ships to neither package exports
// nor the tarball.

/**
 * The string value of `name` in `source` - either `name = "value"` or the
 * typed `name SomeType = "value"` form - or a thrown error naming both.
 * `where` labels the source in that error.
 */
export function goConstantValue(source: string, name: string, where: string): string {
  // The name must sit in declaration position - first token on a line, or
  // right after the const keyword - so it can match neither a SUFFIX of a
  // longer constant (appwire/errors.go's ErrorMutationOutcomeUnknown before
  // MutationOutcomeUnknown is the live collision) nor another declaration's
  // TYPE-token slot (searching for the MutationOutcome type must find no
  // constant, not bind MutationOutcomeUnknown's value). goConstants.test.ts
  // pins both.
  const match = source.match(new RegExp(`(?:^|\\n)\\s*(?:const\\s+)?${name}(?:\\s+\\w+)?\\s*=\\s*"([^"]+)"`));
  const value = match?.[1];
  if (value === undefined) throw new Error(`${where} has no ${name} constant`);
  return value;
}
