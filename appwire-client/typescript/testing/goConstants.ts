// Test-only extractor for Go-source constant bindings: reads one constant's
// value out of `?raw`-imported Go source, so a client test pins its
// discriminant to the daemon's own constant and a Go-side rename or value
// change fails the test instead of silently leaving the client matching a
// discriminator the hub no longer sends. Shared by the errors, warnings, and
// mobile conversation suites; test-support only - the
// @evener/appwire-client/testing subpath ships to neither package exports
// nor the tarball.

/**
 * The string value of `name` in `source` - either `name = "value"` or the
 * typed `name SomeType = "value"` form - or a thrown error naming both.
 * `where` labels the source in that error.
 */
export function goConstantValue(source: string, name: string, where: string): string {
  const match = source.match(new RegExp(`${name}(?:\\s+\\w+)?\\s*=\\s*"([^"]+)"`));
  const value = match?.[1];
  if (value === undefined) throw new Error(`${where} has no ${name} constant`);
  return value;
}
