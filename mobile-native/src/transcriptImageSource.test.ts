import { expect, it } from "vitest";
import { transcriptImageSource } from "./transcriptImageSource";

it("preserves inline image bytes without attaching hub credentials", () => {
  expect(
    transcriptImageSource(
      "data:image/png;base64,AQID",
      "https://hub.test",
      "secret",
    ),
  ).toEqual({ uri: "data:image/png;base64,AQID" });
});
it("resolves hub image routes with only their own origin credentials", () => {
  expect(
    transcriptImageSource(
      "/s/session/images/hash",
      "https://hub.test",
      "secret",
    ),
  ).toEqual({
    uri: "https://hub.test/s/session/images/hash",
    headers: { Authorization: "Bearer secret" },
  });
  expect(
    transcriptImageSource(
      "https://other.test/image.png",
      "https://hub.test",
      "secret",
    ),
  ).toEqual({ uri: "https://other.test/image.png" });
});
it("rejects device paths and embedded URL credentials as image sources", () => {
  for (const source of [
    "file:///private/image.png",
    "javascript:alert(1)",
    "https://name:password@hub.test/image.png",
    "data:text/html;base64,AQID",
    "",
  ])
    expect(() =>
      transcriptImageSource(source, "https://hub.test", "secret"),
    ).toThrow();
});
