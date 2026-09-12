// Browser-storage secret scan for the spawn browser guards.
//
// Storage is a live object whose entries are NOT own enumerable properties:
// JSON.stringify(localStorage) serializes to "{}", so a guard built on it can
// never see a credential that leaked into storage and silently passes. Walk the
// keys the Storage interface actually exposes instead.
export function storageHasSecret(storage: Storage, secret: string): boolean {
  // includes("") is true for any string, so an empty secret would match every
  // non-empty storage value: fail closed instead of false-positiving.
  if (secret === "") return false;
  for (let index = 0; index < storage.length; index += 1) {
    const key = storage.key(index);
    if (key === null) continue;
    const value = storage.getItem(key);
    if (value?.includes(secret)) return true;
  }
  return false;
}
