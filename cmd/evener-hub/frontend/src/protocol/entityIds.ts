// Mirrors identifier/job.go, identifier/uuid.go, identifier/domains.go. The
// server is authoritative; this module is pinned to it and its tests use the
// same golden ids.
export type EntityKind = "job" | "delegate" | "watch";

export interface EntityIdMatch {
  kind: EntityKind;
  id: string;
  start: number;
  end: number;
}

const BASE62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";
const BASE62_SET = new Set(BASE62.split(""));
const UUID_WIDTH = 22;
const JOB_SUFFIX = 12;

function isBase62Run(value: string): boolean {
  for (const ch of value) if (!BASE62_SET.has(ch)) return false;
  return true;
}

// Decodes base62 to bytes and enforces the UUIDv7 version/variant bits the
// server checks (ValidateUUIDv7Payload). Anything that does not decode to
// <=128 bits, or is not version 7 / RFC4122 variant, is not an id.
function isUUIDv7Payload(payload: string): boolean {
  if (payload.length !== UUID_WIDTH || !isBase62Run(payload)) return false;
  let n = 0n;
  for (const ch of payload) n = n * 62n + BigInt(BASE62.indexOf(ch));
  if (n >> 128n !== 0n) return false;
  const bytes = new Uint8Array(16);
  for (let i = 15; i >= 0; i--) {
    bytes[i] = Number(n & 0xffn);
    n >>= 8n;
  }
  const version = bytes[6] >> 4;
  const variant = bytes[8] >> 6;
  return version === 7 && variant === 2;
}

function matchAt(text: string, start: number, kind: EntityKind, length: number): EntityIdMatch | undefined {
  const end = start + length;
  if (end > text.length) return undefined;
  const before = start === 0 ? "" : text[start - 1];
  const after = end === text.length ? "" : text[end];
  if (before !== "" && /[0-9A-Za-z_]/.test(before)) return undefined;
  if (after !== "" && /[0-9A-Za-z_]/.test(after)) return undefined;
  const id = text.slice(start, end);
  if (!isValidId(kind, id)) return undefined;
  return { kind, id, start, end };
}

function isValidId(kind: EntityKind, id: string): boolean {
  if (kind === "delegate") return id.startsWith("dlg_") && isUUIDv7Payload(id.slice(4));
  if (kind === "watch") return id.startsWith("watch_") && isUUIDv7Payload(id.slice(6));
  if (!id.startsWith("job_")) return false;
  const owner = id.slice(4, 4 + UUID_WIDTH);
  const suffix = id.slice(5 + UUID_WIDTH);
  return (
    id.length === 5 + UUID_WIDTH + JOB_SUFFIX &&
    id[4 + UUID_WIDTH] === "_" &&
    isUUIDv7Payload(owner) &&
    isBase62Run(suffix)
  );
}

export function findEntityIds(text: string): EntityIdMatch[] {
  const out: EntityIdMatch[] = [];
  // Cheap prefix prefilter: no decode unless the prefix is present.
  const patterns: Array<[EntityKind, string, number]> = [
    ["job", "job_", 5 + UUID_WIDTH + JOB_SUFFIX],
    ["delegate", "dlg_", 4 + UUID_WIDTH],
    ["watch", "watch_", 6 + UUID_WIDTH],
  ];
  for (let i = 0; i < text.length; i++) {
    for (const [kind, prefix, length] of patterns) {
      if (!text.startsWith(prefix, i)) continue;
      const match = matchAt(text, i, kind, length);
      if (match) {
        out.push(match);
        i = match.end - 1;
      }
      break;
    }
  }
  return out;
}

export function entityKindOf(id: string): EntityKind | undefined {
  if (isValidId("delegate", id)) return "delegate";
  if (isValidId("watch", id)) return "watch";
  if (isValidId("job", id)) return "job";
  return undefined;
}

export function jobOwnerSessionId(jobId: string): string | undefined {
  return isValidId("job", jobId) ? jobId.slice(4, 4 + UUID_WIDTH) : undefined;
}
