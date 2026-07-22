// Attachment size/count/type limits (parity-m5-composer.md §G, contracts
// §Attachments), ported verbatim from composer-attachments.js's own
// attachmentRejection: max 8 attachments, max 8 MiB per file. The 8-count
// cap is CUMULATIVE across the whole composer session (paste + drag +
// file-picker share one running total) - callers pass the count already
// reserved so far, not a fresh read of "how many are there right now",
// since a batch (e.g. dropping several files at once) must count each
// prior file in the SAME batch too.
export const MAX_ATTACHMENTS = 8;
export const MAX_ATTACHMENT_BYTES = 8 * 1024 * 1024;

export interface RejectableFile {
  type: string;
  size: number;
  name: string;
}

// rejectionReason returns undefined when `file` is acceptable, or a
// user-facing message naming the file and the specific limit it broke.
// Branch order matches the legacy helper exactly: non-image type first
// (a bare filename - no limit to name), then the count cap, then size -
// so a file that breaks both count and size reports the count rejection.
export function rejectionReason(file: RejectableFile, reservedCount: number): string | undefined {
  const name = file.name || "unknown";
  if (!file.type.startsWith("image/")) return name;
  if (reservedCount >= MAX_ATTACHMENTS) return `${name} (maximum ${MAX_ATTACHMENTS} images)`;
  if (file.size > MAX_ATTACHMENT_BYTES) return `${name} (maximum 8 MB)`;
  return undefined;
}
