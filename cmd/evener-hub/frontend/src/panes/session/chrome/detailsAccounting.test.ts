// @vitest-environment node

import { expect, test } from "vitest";
import { formatTimestamp } from "./detailsAccounting";

test("formatTimestamp renders an ISO instant in the reader's own locale", () => {
  const iso = "2026-07-23T18:02:46.000Z";
  expect(formatTimestamp(iso)).toBe(new Date(iso).toLocaleString());
});

test("formatTimestamp reports an unparseable instant as absent rather than 'Invalid Date'", () => {
  expect(formatTimestamp("not-a-date")).toBeUndefined();
  expect(formatTimestamp(undefined)).toBeUndefined();
});
