import { createHash } from "node:crypto";

function digest(value) {
  return createHash("sha256").update(value).digest("hex");
}

export function summarizeAuditedUrl(value) {
  const url = String(value);
  if (url.startsWith("data:")) {
    const comma = url.indexOf(",");
    const metadata = comma === -1 ? url.slice(5) : url.slice(5, comma);
    const payload = comma === -1 ? "" : url.slice(comma + 1);
    const tokens = metadata.split(";").filter(Boolean);
    const declaredMime = tokens[0]?.includes("/") ? tokens[0] : null;
    const normalizedMime = (declaredMime ?? "text/plain").toLowerCase();
    const mime =
      normalizedMime.length <= 127 &&
      /^[a-z0-9!#$&^_.+*-]+\/[a-z0-9!#$&^_.+*-]+$/.test(normalizedMime)
        ? normalizedMime
        : "invalid";
    return {
      scheme: "data:",
      mime,
      encoding: tokens.some((token) => token.toLowerCase() === "base64")
        ? "base64"
        : "percent",
      encodedLength: payload.length,
      sha256: digest(url),
    };
  }
  if (url.startsWith("blob:")) {
    return {
      scheme: "blob:",
      mime: null,
      encodedLength: url.length - "blob:".length,
      sha256: digest(url),
    };
  }
  return url;
}

export function classifyAuditedRequest(value, expectedOrigin) {
  const url = String(value);
  let offOrigin = false;
  try {
    const parsed = new URL(url);
    offOrigin =
      !["data:", "blob:"].includes(parsed.protocol) &&
      parsed.origin !== expectedOrigin;
  } catch {
    offOrigin = true;
  }
  return {
    offOrigin,
    audit: summarizeAuditedUrl(url),
  };
}
