// @vitest-environment node

import { expect, test } from "vitest";

import { translateAttachmentMarkers } from "./attachmentMarkers";

test("translates a marker to prose naming the staged attachment", () => {
  const text = translateAttachmentMarkers("[image 1]Describe the attached image", [{ name: "shot.png" }]);

  expect(text).toBe("(attached image 1: shot.png)Describe the attached image");
});

test("omits the name clause - and its separator - when the staged attachment has no name", () => {
  const text = translateAttachmentMarkers("look: [image 1]", [{}]);

  expect(text).toBe("look: (attached image 1)");
});

test("returns marker-less text byte-identical: untrimmed, newlines intact", () => {
  const original = "  keep\n  every\n\n  byte  ";

  expect(translateAttachmentMarkers(original, [{ name: "shot.png" }])).toBe(original);
});

test("leaves a marker verbatim when nothing is staged - translating it would fabricate an attachment", () => {
  expect(translateAttachmentMarkers("[image 1]hi", [])).toBe("[image 1]hi");
  expect(translateAttachmentMarkers("[image 1]hi")).toBe("[image 1]hi");
});

test("leaves the surplus markers verbatim when the text carries more of them than there are attachments", () => {
  const text = translateAttachmentMarkers("[image 1][image 2]", [{ name: "a.png" }]);

  expect(text).toBe("(attached image 1: a.png)[image 2]");
});

test("a removed attachment's marker gap still names the right images", () => {
  // Removing the first of three staged images strips its marker from the text
  // and its item from the list: markers 2,3 over attachments [b, c].
  const text = translateAttachmentMarkers("[image 2][image 3]", [{ name: "b.png" }, { name: "c.png" }]);

  expect(text).toBe("(attached image 2: b.png)(attached image 3: c.png)");
});

test("maps by marker number, not by order of appearance in the text", () => {
  // Attaching a second image with the cursor moved back to the start puts
  // marker 2 ahead of marker 1 in the text while the list stays [a, b].
  const text = translateAttachmentMarkers("[image 2] then [image 1]", [{ name: "a.png" }, { name: "b.png" }]);

  expect(text).toBe("(attached image 2: b.png) then (attached image 1: a.png)");
});

test("translates every occurrence of a repeated marker identically", () => {
  const text = translateAttachmentMarkers("[image 1] and again [image 1]", [{ name: "a.png" }]);

  expect(text).toBe("(attached image 1: a.png) and again (attached image 1: a.png)");
});

test("leaves near-miss marker syntax alone", () => {
  const original = "[image] [image one] [ image 1 ] [Image 1]";

  expect(translateAttachmentMarkers(original, [{ name: "a.png" }])).toBe(original);
});
