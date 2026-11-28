import { formatMonthDay } from "../../../widgets/timestamp/formatAbsoluteTime";

const DATE_WITH_YEAR = new Intl.DateTimeFormat("en-US", {
  year: "numeric",
  month: "short",
  day: "numeric",
});

const CLOCK_24H = new Intl.DateTimeFormat("en-US", {
  hour: "2-digit",
  minute: "2-digit",
  hour12: false,
});

/**
 * Formats an ISO-8601 timestamp as a compact 24-hour date/time label (e.g.
 * "Nov 25, 07:15" or "Nov 25, 2025, 07:15") for Settings -> Hub.
 * Returns null when `iso` is missing or unparseable.
 */
export function formatHubStamp(iso: string | undefined, options: { omitCurrentYear?: boolean } = {}): string | null {
  if (!iso) return null;
  const parsed = Date.parse(iso);
  if (Number.isNaN(parsed)) return null;
  const date = new Date(parsed);
  const omitYear = options.omitCurrentYear === true && date.getFullYear() === new Date().getFullYear();
  const dayPart = omitYear ? formatMonthDay(parsed) : DATE_WITH_YEAR.format(date);
  return `${dayPart}, ${CLOCK_24H.format(date)}`;
}
