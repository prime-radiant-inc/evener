// Stubs a host global's property with a getter that throws on access itself
// (not just on a method call), for suites proving a shim survives a
// sandboxed page. Returns a function that restores the original descriptor.
export function stubThrowingGetter(target: object, prop: string): () => void {
  const original = Object.getOwnPropertyDescriptor(target, prop);
  Object.defineProperty(target, prop, {
    configurable: true,
    get(): never {
      throw new Error(`${prop} access denied`);
    },
  });
  return () => {
    if (original) {
      Object.defineProperty(target, prop, original);
    } else {
      delete (target as Record<string, unknown>)[prop];
    }
  };
}
