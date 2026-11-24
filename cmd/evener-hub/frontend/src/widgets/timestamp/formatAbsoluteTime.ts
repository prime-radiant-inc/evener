// One absolute-time formatter for the surfaces that show an instant. Three
// shapes preserve every existing visible format:
//
//   adaptive    pinned "en"; time-only on the reference calendar day, then
//               date+time with seconds (Timestamp's hover title)
//   locale      the reader's locale and timezone, date+time (session details,
//               a marketplace's "Last updated")
//   localeTime  the reader's locale and timezone, clock time only (the
//               launch-config "Saved at" status)
//
// The adaptive shape stays deterministic across environments; the locale shapes
// exist where "when did this happen for me" is the point. Formatters are
// module singletons because Intl construction with an options object is not
// cached, and a hover title formats on every render of every visible
// timestamp.

export type AbsoluteTimeFormat = "adaptive" | "locale" | "localeTime";

export interface FormatAbsoluteTimeOptions {
  /** Output shape; defaults to "adaptive". */
  format?: AbsoluteTimeFormat;
  /** Epoch-ms reference instant for `adaptive`'s same-calendar-day test.
   * Defaults to the current clock; callers that own "now" pass it. */
  now?: number;
}

const TIME = new Intl.DateTimeFormat("en", {
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hour12: false,
});
const DATE_TIME = new Intl.DateTimeFormat("en", {
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hour12: false,
});
const MONTH_DAY = new Intl.DateTimeFormat("en-US", { month: "short", day: "numeric" });

export function formatMonthDay(value: number): string {
  return MONTH_DAY.format(value);
}

/** formatAbsoluteTime renders an epoch-ms instant in the shape `format`
 * names; see this module's header. `value` is expected to be a finite epoch:
 * callers own their own absence and parse-failure policy. */
export function formatAbsoluteTime(value: number, options: FormatAbsoluteTimeOptions = {}): string {
  const { format = "adaptive", now = Date.now() } = options;
  switch (format) {
    case "locale":
      return new Date(value).toLocaleString();
    case "localeTime":
      return new Date(value).toLocaleTimeString();
    default:
      return new Date(value).toDateString() === new Date(now).toDateString()
        ? TIME.format(value)
        : DATE_TIME.format(value);
  }
}
