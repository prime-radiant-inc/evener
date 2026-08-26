function requireNonNegativeFinite(value: number, label: string): void {
  if (!Number.isFinite(value) || value < 0) {
    throw new RangeError(`${label} must be a non-negative finite number`);
  }
}

export function formatRelativeTime(label: string): string {
  const formatted = label.trim();
  if (formatted.length === 0) {
    throw new RangeError("relative time label must not be empty");
  }
  return formatted;
}

export function formatDuration(milliseconds: number): string {
  requireNonNegativeFinite(milliseconds, "milliseconds");
  const seconds = Math.floor(milliseconds / 1_000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) {
    const remainingSeconds = seconds % 60;
    return remainingSeconds === 0
      ? `${minutes}m`
      : `${minutes}m ${remainingSeconds}s`;
  }
  const hours = Math.floor(minutes / 60);
  const remainingMinutes = minutes % 60;
  return remainingMinutes === 0
    ? `${hours}h`
    : `${hours}h ${remainingMinutes}m`;
}

export function formatUsage(tokens: number): string {
  requireNonNegativeFinite(tokens, "tokens");
  if (!Number.isSafeInteger(tokens)) {
    throw new RangeError("tokens must be a safe integer");
  }
  const units = [
    { threshold: 1_000_000, suffix: "M" },
    { threshold: 1_000, suffix: "K" },
  ] as const;
  const unit = units.find(({ threshold }) => tokens >= threshold);
  const quantity = unit
    ? `${Number((tokens / unit.threshold).toFixed(1))}${unit.suffix}`
    : String(tokens);
  return `${quantity} ${tokens === 1 ? "token" : "tokens"}`;
}

export function basename(path: string): string {
  if (path.length === 0 || /^[/\\]+$/.test(path)) return path;
  const withoutTrailingSeparators = path.replace(/[/\\]+$/, "");
  return withoutTrailingSeparators.split(/[/\\]/).at(-1) ?? "";
}
