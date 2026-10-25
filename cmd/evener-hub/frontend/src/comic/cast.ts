export type ComicCharacter = "anna" | "xeno" | "armando" | "bolo" | "denise" | "tiki";

export type ComicPersona = {
  key: string;
  character: ComicCharacter;
  name: string;
  badge: string;
  ownerRef?: string;
};

const SUPPORTING_CAST = ["armando", "bolo", "denise", "tiki"] as const;

/** JSON's array encoding preserves both complete strings, including embedded separators. */
export function comicDelegateKey(ownerRef: string, delegateId: string): string {
  return JSON.stringify([ownerRef, delegateId]);
}

export function comicDelegatePersona(ownerRef: string, delegateId: string, name?: string): ComicPersona {
  const key = comicDelegateKey(ownerRef, delegateId);
  // FNV-1a over UTF-16 code units chooses art only. Identity always uses the full tuple.
  let hash = 0x811c9dc5;
  for (let i = 0; i < key.length; i += 1) {
    hash = Math.imul(hash ^ key.charCodeAt(i), 0x01000193) >>> 0;
  }
  return {
    key,
    character: SUPPORTING_CAST[hash % SUPPORTING_CAST.length] ?? "armando",
    name: name?.trim() || delegateId,
    badge: delegateId,
    ownerRef,
  };
}
