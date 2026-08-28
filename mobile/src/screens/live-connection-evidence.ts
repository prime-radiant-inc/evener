export async function digestProfileOrigin(
  origin: string,
  subtle: SubtleCrypto | null | undefined = globalThis.crypto?.subtle,
): Promise<string> {
  if (subtle == null) {
    throw new Error("origin digest unavailable");
  }
  const bytes = new TextEncoder().encode(origin);
  const result = await subtle.digest("SHA-256", bytes);
  const hex = [...new Uint8Array(result)]
    .map((value) => value.toString(16).padStart(2, "0"))
    .join("");
  return `sha256:${hex}`;
}
