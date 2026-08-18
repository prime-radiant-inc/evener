// @vitest-environment node

import { describe, expect, test } from "vitest";
import { readUrlPrefill } from "./urlPrefill";

describe("readUrlPrefill (spec §5, ?dir=/?prompt=)", () => {
  test("reads both dir and prompt from the query string", () => {
    expect(readUrlPrefill("?dir=%2Fhome%2Fme&prompt=fix%20the%20bug")).toEqual({
      dir: "/home/me",
      prompt: "fix the bug",
    });
  });

  test("returns only the keys that are present", () => {
    expect(readUrlPrefill("?dir=/x")).toEqual({ dir: "/x" });
    expect(readUrlPrefill("?prompt=hi")).toEqual({ prompt: "hi" });
  });

  test("an empty search yields no prefill", () => {
    expect(readUrlPrefill("")).toEqual({});
    expect(readUrlPrefill("?")).toEqual({});
  });

  test("an empty-valued param is ignored (dir= with nothing after it)", () => {
    expect(readUrlPrefill("?dir=&prompt=go")).toEqual({ prompt: "go" });
  });

  test("preserves a prompt's raw whitespace and newlines (decoded verbatim)", () => {
    expect(readUrlPrefill("?prompt=%20%20keep%0Alines")).toEqual({ prompt: "  keep\nlines" });
  });
});
