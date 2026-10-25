export type ComicRoute = { kind: "picker" } | { kind: "session"; ref: string } | { kind: "invalid" };

export function comicSessionURL(ref: string): string {
  return `/comic/s/${encodeURIComponent(ref)}`;
}

export function parseComicRoute(pathname: string): ComicRoute | null {
  if (pathname === "/comic" || pathname === "/comic/") return { kind: "picker" };
  if (pathname !== "/comic" && !pathname.startsWith("/comic/")) return null;
  if (!pathname.startsWith("/comic/s/")) return { kind: "invalid" };
  const encoded = pathname.slice("/comic/s/".length);
  if (!encoded || encoded.includes("/")) return { kind: "invalid" };
  try {
    const ref = decodeURIComponent(encoded);
    return ref ? { kind: "session", ref } : { kind: "invalid" };
  } catch {
    return { kind: "invalid" };
  }
}
