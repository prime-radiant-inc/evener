import type { BoundedDisplayText } from "./model";

export type { BoundedDisplayText } from "./model";

export const TRUNCATION_MARKER = "… truncated";

export const DISPLAY_LIMITS = Object.freeze({
  titleProject: 256,
  statusUpdated: 128,
  feedLabel: 128,
  detailLabel: 256,
  userMessage: 64 * 1024,
  assistantProse: 64 * 1024,
  questionHeader: 128,
  questionPrompt: 8 * 1024,
  questionOptionLabel: 1024,
  questionOptionDetail: 4 * 1024,
  questionSupportingText: 4 * 1024,
  failureTitle: 256,
  failureBody: 8 * 1024,
  failureDetail: 64 * 1024,
  noticePreview: 512,
  noticeDetail: 16 * 1024,
  hiddenPreview: 0,
  hiddenDetail: 0,
  lifecycleDetail: 16 * 1024,
  reasoningPreview: 512,
  reasoningDetail: 64 * 1024,
  toolPreview: 512,
  toolDetail: 64 * 1024,
  attachmentLabel: 512,
  attachmentMetadata: 4 * 1024,
  diagnosticPreview: 256,
  diagnosticDetail: 16 * 1024,
  unknownPreview: 0,
  unknownDetail: 16 * 1024,
  evidenceHeading: 128,
  error: 1024,
} as const);

const encoder = new TextEncoder();
const markerUtf8Bytes = encoder.encode(TRUNCATION_MARKER).length;

const CREDENTIAL_MARKER = "[redacted:credential]";
const AUTHORIZATION_MARKER = "[redacted:authorization]";
const PROFILE_ID_MARKER = "[redacted:profile-id]";
const OPERATIONAL_ID_MARKER = "[redacted:operational-id]";
const PATH_MARKER = "[redacted:path]";

function redactAuthorizationUrls(source: string): string {
  return source.replace(/https?:\/\/[^\s<>"']+/giu, (candidate) => {
    let url: URL;
    try {
      url = new URL(candidate);
    } catch {
      return candidate;
    }
    const sensitive =
      /(?:authorize|authorization|oauth)/iu.test(url.pathname) ||
      [...url.searchParams.keys()].some((key) =>
        /^(?:authorization|client_id|code|token|access_token|refresh_token|redirect_uri|state|key|secret)$/iu.test(
          key,
        ),
      );
    if (!sensitive) return candidate;
    return `${url.origin}${url.pathname}?${AUTHORIZATION_MARKER}`;
  });
}

function redactSensitiveText(source: string): string {
  let safe = source.replace(
    /\b(?:Bearer|Basic)\s+[A-Za-z0-9._~+/=-]+/giu,
    CREDENTIAL_MARKER,
  );
  safe = safe.replace(
    /\b((?:api[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret|token|password|passwd|secret)["']?\s*[:=]\s*)(["']?)[^\s,"';&}]+\2/giu,
    (_match, prefix: string) => `${prefix}${CREDENTIAL_MARKER}`,
  );
  safe = redactAuthorizationUrls(safe);
  safe = safe.replace(
    /\b(profile(?:Id|_id|-id)?\s*[:=]\s*)["']?[^\s,"';&}]+/giu,
    (_match, prefix: string) => `${prefix}${PROFILE_ID_MARKER}`,
  );
  safe = safe.replace(
    /\b((?:thread|job|delegate|call)(?:Id|_id|-id)?\s*[:=]\s*)["']?[^\s,"';&}]+/giu,
    (_match, prefix: string) => `${prefix}${OPERATIONAL_ID_MARKER}`,
  );
  safe = safe.replace(
    /\b(?<!redacted:)(?:profile|thread|job|delegate|call)[_-][A-Za-z0-9][A-Za-z0-9._:-]*\b(?!\s*[:=])/giu,
    (identifier) =>
      identifier.toLowerCase().startsWith("profile")
        ? PROFILE_ID_MARKER
        : OPERATIONAL_ID_MARKER,
  );
  safe = safe.replace(
    /(^|[\s"'(=])(\/(?!\/)[^\s"'<>),;]+)/gmu,
    (_match, prefix: string) => `${prefix}${PATH_MARKER}`,
  );
  safe = safe.replace(/\b[A-Za-z]:\\[^\s"'<>|]+/gu, PATH_MARKER);
  return safe;
}

function buildKmpFailure(marker: string): number[] {
  const failure = new Array<number>(marker.length).fill(0);
  let prefixLength = 0;
  for (let index = 1; index < marker.length; index += 1) {
    while (prefixLength > 0 && marker[index] !== marker[prefixLength]) {
      prefixLength = failure[prefixLength - 1] ?? 0;
    }
    if (marker[index] === marker[prefixLength]) prefixLength += 1;
    failure[index] = prefixLength;
  }
  return failure;
}

function stripAllMarkersLinear(source: string, marker: string): string {
  if (marker.length === 0) return source;
  const failure = buildKmpFailure(marker);
  const stack: Array<[string, number]> = [];
  for (const character of source) {
    let state = stack.at(-1)?.[1] ?? 0;
    while (state > 0 && character !== marker[state]) {
      state = failure[state - 1] ?? 0;
    }
    if (character === marker[state]) state += 1;
    if (state === marker.length) {
      stack.length -= marker.length - 1;
    } else {
      stack.push([character, state]);
    }
  }
  return stack.map(([character]) => character).join("");
}

function decodeValidPrefix(encoded: Uint8Array, maxUtf8Bytes: number): string {
  const decoder = new TextDecoder("utf-8", { fatal: true });
  let end = Math.min(maxUtf8Bytes, encoded.length);
  while (end > 0) {
    try {
      return decoder.decode(encoded.subarray(0, end));
    } catch {
      end -= 1;
    }
  }
  return "";
}

export function boundDisplayText(
  source: string,
  maxUtf8Bytes: number,
  policy: "plain" | "redacted",
): BoundedDisplayText {
  const originalUtf8Bytes = encoder.encode(source).length;
  const safe = policy === "redacted" ? redactSensitiveText(source) : source;
  const normalized = stripAllMarkersLinear(safe, TRUNCATION_MARKER);
  const encoded = encoder.encode(normalized);
  if (encoded.length <= maxUtf8Bytes) {
    return { text: normalized, truncated: false, originalUtf8Bytes };
  }
  if (maxUtf8Bytes < markerUtf8Bytes) {
    throw new RangeError("display limit cannot contain truncation marker");
  }
  const budget = maxUtf8Bytes - markerUtf8Bytes;
  return {
    text: `${decodeValidPrefix(encoded, budget)}${TRUNCATION_MARKER}`,
    truncated: true,
    originalUtf8Bytes,
  };
}
