// deferred hands a test the resolver of a promise it scripts into a fake, so
// the test decides when a request answers: create it before the request goes
// out, script the fake to return `promise`, and call `resolve` when the
// ordering under test is set up. In-repo test support, not shipped.

export interface Deferred<T> {
  promise: Promise<T>;
  resolve: (value: T) => void;
}

export function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((complete) => {
    resolve = complete;
  });
  return { promise, resolve };
}
