import { describe, expect, it } from "vitest";
import {
  boundDisplayText,
  DISPLAY_LIMITS,
  TRUNCATION_MARKER,
} from "./display-text";

const encoder = new TextEncoder();
const utf8Bytes = (value: string): number => encoder.encode(value).length;
const markerCount = (value: string): number =>
  value.split(TRUNCATION_MARKER).length - 1;

const EXPECTED_LIMITS = {
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
} as const;

describe("boundDisplayText", () => {
  it("exports the exact shared UTF-8 display limits", () => {
    expect(DISPLAY_LIMITS).toEqual(EXPECTED_LIMITS);
    expect(Object.isFrozen(DISPLAY_LIMITS)).toBe(true);
  });

  it.each(Object.entries(EXPECTED_LIMITS).filter(([, cap]) => cap > 0))(
    "keeps %s within its %i-byte cap at split four-byte emoji edges",
    (_name, cap) => {
      const exact = boundDisplayText("a".repeat(cap), cap, "plain");
      expect(exact).toEqual({
        text: "a".repeat(cap),
        truncated: false,
        originalUtf8Bytes: cap,
      });

      const truncationBudget = cap - utf8Bytes(TRUNCATION_MARKER);
      const splitEdge = `${"a".repeat(truncationBudget - 1)}🎉${"z".repeat(utf8Bytes(TRUNCATION_MARKER))}`;
      const bounded = boundDisplayText(splitEdge, cap, "plain");
      expect(bounded.truncated).toBe(true);
      expect(bounded.originalUtf8Bytes).toBe(utf8Bytes(splitEdge));
      expect(utf8Bytes(bounded.text)).toBeLessThanOrEqual(cap);
      expect(bounded.text).not.toContain("�");
      expect(bounded.text.endsWith(TRUNCATION_MARKER)).toBe(true);
      expect(markerCount(bounded.text)).toBe(1);
    },
  );

  it("handles empty input without inventing truncation", () => {
    expect(boundDisplayText("", DISPLAY_LIMITS.feedLabel, "plain")).toEqual({
      text: "",
      truncated: false,
      originalUtf8Bytes: 0,
    });
  });

  it.each([
    ["zero", "ordinary content"],
    ["one", `before ${TRUNCATION_MARKER} after`],
    ["multiple", `a${TRUNCATION_MARKER}b${TRUNCATION_MARKER}c`],
  ])("normalizes %s literal markers in within-cap input", (_name, source) => {
    const result = boundDisplayText(source, 256, "plain");
    expect(result.truncated).toBe(false);
    expect(markerCount(result.text)).toBe(0);
  });

  it.each([
    ["zero", "x".repeat(400)],
    ["one", `${TRUNCATION_MARKER}${"x".repeat(400)}`],
    ["multiple", `${TRUNCATION_MARKER}x${TRUNCATION_MARKER}${"x".repeat(400)}`],
  ])("normalizes %s literal markers in over-cap input", (_name, source) => {
    const result = boundDisplayText(source, 128, "plain");
    expect(result.truncated).toBe(true);
    expect(utf8Bytes(result.text)).toBeLessThanOrEqual(128);
    expect(markerCount(result.text)).toBe(1);
    expect(result.text.endsWith(TRUNCATION_MARKER)).toBe(true);
  });

  it.each([
    ["bearer", "Bearer super-secret-token", "[redacted:credential]"],
    ["basic", "Basic dXNlcjpwYXNzd29yZA==", "[redacted:credential]"],
    ["token field", "token=super-secret-token", "[redacted:credential]"],
    ["password field", 'password: "hunter2"', "[redacted:credential]"],
    ["secret field", "client_secret=abc123", "[redacted:credential]"],
    [
      "authorization URL",
      "https://host.test/callback?code=secret-code&state=opaque-state",
      "[redacted:authorization]",
    ],
    [
      "OAuth authorization URL",
      "https://host.test/oauth/authorize?client_id=public-client&redirect_uri=https%3A%2F%2Flocalhost%2Fcallback",
      "[redacted:authorization]",
    ],
    ["profile ID", "profileId=profile-private-123", "[redacted:profile-id]"],
    ["thread ID", "thread_id=thread-private-123", "[redacted:operational-id]"],
    ["job ID", "job_id=job-private-123", "[redacted:operational-id]"],
    [
      "delegate ID",
      "delegate_id=delegate-private-123",
      "[redacted:operational-id]",
    ],
    ["call ID", "callId=call-private-123", "[redacted:operational-id]"],
    ["home path", "/Users/alice/private/project/file.txt", "[redacted:path]"],
    ["filesystem path", "/var/private/secret.sock", "[redacted:path]"],
  ])("redacts %s with a fixed typed marker", (_name, source, marker) => {
    const result = boundDisplayText(source, 1024, "redacted");
    expect(result.text).toContain(marker);
    expect(result.text.split(marker).length - 1).toBe(1);
    expect(result.text).not.toContain("private-123");
    expect(result.text).not.toContain("super-secret");
    expect(result.text).not.toContain("hunter2");
    expect(result.originalUtf8Bytes).toBe(utf8Bytes(source));
  });

  it("redacts before marker normalization and truncation", () => {
    const source = `${"x".repeat(100)} token=secret-across-boundary ${TRUNCATION_MARKER} tail`;
    const result = boundDisplayText(source, 128, "redacted");
    expect(result.truncated).toBe(true);
    expect(result.text).not.toContain("secret-across-boundary");
    expect(markerCount(result.text)).toBe(1);
    expect(utf8Bytes(result.text)).toBeLessThanOrEqual(128);
  });
});
