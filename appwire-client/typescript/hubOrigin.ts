// Hub endpoint grammar is shared by native setup, pairing and the dev proxy.
// Check the literal input before URL: WHATWG repairs dot paths, numeric hosts,
// empty delimiters and backslashes which must instead be rejected.
export type HubOriginCode =
  | "empty"
  | "control"
  | "scheme"
  | "host"
  | "userinfo"
  | "path"
  | "query"
  | "fragment"
  | "wildcard"
  | "scoped_host"
  | "port";

export class HubOriginError extends Error {
  constructor(
    readonly code: HubOriginCode,
    readonly field: string,
  ) {
    super(`invalid ${field} (${code})`);
    this.name = "HubOriginError";
  }
}

function invalid(code: HubOriginCode, field: string): never {
  throw new HubOriginError(code, field);
}

/** Validate a literal HTTP(S) origin and return its browser-compatible canonical form. */
export function parseHubOrigin(raw: string): string {
  if (raw === "") invalid("empty", "origin");
  if (/[\p{Cc}\p{White_Space}]/u.test(raw)) invalid("control", "origin");
  if (raw.includes("\\")) invalid("path", "path");
  const schemeEnd = raw.indexOf("://");
  if (schemeEnd <= 0) invalid("scheme", "scheme");
  const scheme = raw.slice(0, schemeEnd).toLowerCase();
  if (scheme !== "http" && scheme !== "https") invalid("scheme", "scheme");
  const rest = raw.slice(schemeEnd + 3);
  if (rest.includes("?")) invalid("query", "query");
  if (rest.includes("#")) invalid("fragment", "fragment");
  const slash = rest.indexOf("/");
  const authority = slash < 0 ? rest : rest.slice(0, slash);
  if (slash >= 0 && rest.slice(slash) !== "/") invalid("path", "path");
  if (!authority) invalid("host", "host");
  if (authority.includes("@")) invalid("userinfo", "userinfo");

  let rawHost: string;
  let rawPort: string | undefined;
  if (authority.startsWith("[")) {
    const closing = authority.indexOf("]");
    if (closing < 0) invalid("host", "host");
    rawHost = authority.slice(1, closing);
    const suffix = authority.slice(closing + 1);
    if (suffix && !suffix.startsWith(":")) invalid("host", "host");
    if (suffix) rawPort = suffix.slice(1);
    if (rawHost.includes("%")) invalid("scoped_host", "host");
  } else {
    const colon = authority.indexOf(":");
    if (colon >= 0) {
      if (authority.indexOf(":", colon + 1) >= 0) invalid("host", "host");
      rawHost = authority.slice(0, colon);
      rawPort = authority.slice(colon + 1);
    } else rawHost = authority;
  }
  if (!rawHost) invalid("host", "host");
  if (rawPort !== undefined && (!/^[0-9]+$/.test(rawPort) || Number(rawPort) < 1 || Number(rawPort) > 65535))
    invalid("port", "port");

  // A browser interprets 127.1 and 2130706433 as 127.0.0.1. Reject those
  // spellings while retaining ordinary dotted-decimal IPv4 and DNS names.
  const numeric = rawHost.replace(/\.$/, "");
  if (numeric === "0.0.0.0") invalid("wildcard", "host");
  if (
    !authority.startsWith("[") &&
    /^(?:[0-9]+|0[xX][0-9a-fA-F]+)$/.test(numeric.slice(numeric.lastIndexOf(".") + 1))
  ) {
    if (
      numeric !== rawHost ||
      !/^(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)$/.test(rawHost)
    )
      invalid("host", "host");
    if (rawHost.split(".").some((part) => Number(part) > 255)) invalid("host", "host");
  }

  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    invalid("host", "host");
  }
  if (!url.hostname || url.username || url.password) invalid("host", "host");
  // IDNA can turn Unicode digits into IPv4 after the raw-host checks. Only
  // a literal canonical dotted IPv4 address may normalize to an IPv4 host.
  if (url.hostname === "0.0.0.0") invalid("wildcard", "host");
  if (!authority.startsWith("[") && /^\d+\.\d+\.\d+\.\d+$/.test(url.hostname) && url.hostname !== rawHost)
    invalid("host", "host");
  if (url.hostname === "[::]") invalid("wildcard", "host");
  // URL.hostname includes IPv6 brackets; WHATWG keeps mapped IPv6 in hex form.
  if (url.hostname === "[::ffff:0:0]") invalid("wildcard", "host");
  return url.origin;
}

/** Validate an origin and construct the root AppWire WebSocket endpoint. */
export function hubRPCURL(origin: string): string {
  const normalized = parseHubOrigin(origin);
  return `${normalized.startsWith("https:") ? "wss" : "ws"}${normalized.slice(normalized.indexOf(":"))}/rpc`;
}
