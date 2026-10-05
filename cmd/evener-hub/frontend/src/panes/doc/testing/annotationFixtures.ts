export const SOURCE = "First occurrence: same\n\nOther context for a second occurrence is nearby: same\n";
export const FIRST = { start: 18, end: 22 };
export const SECOND = { start: 73, end: 77 };
export const REFERENCE =
  'A note on `NOTES.md:1:19-1:22` (captured source positions), selecting “same”: "Keep this scope."\n' +
  'Captured whole-file revision: "rev-A".\n' +
  'Context before: "First occurrence: "; after: "\\n\\nOther context for a second occurrence is nearby: same\\n".\n' +
  "Verify the quotation and context against current content before relying on these captured coordinates.";
export const APPENDED = `Existing draft\n\n${REFERENCE}`;
export const JOURNEY_REFERENCE =
  'A note on `NOTES.md:1:19-1:22` (captured source positions), selecting “same”: "Keep this scope."\n' +
  'Captured whole-file revision: "4808d04cf747af87429a6d65d3c229bba77e2ce07a9145e520400d9fdebff0a6".\n' +
  'Context before: "First occurrence: "; after: "".\n' +
  "Verify the quotation and context against current content before relying on these captured coordinates.";
export const JOURNEY_APPENDED = `Existing draft\n\n${JOURNEY_REFERENCE}`;
export const RICH_SOURCE =
  "# Note\r\n\r\nfirst **same** [link](https://example.test) &amp; 😀\r\n\r\n" +
  "second same\r\n\r\n- item **same**\r\n\r\n" +
  "| Cell | Other |\r\n| --- | --- |\r\n| same | tail |\r\n\r\n```ts\r\nsame()\r\n```\r\n";
export const RICH_RANGES = [
  { start: 18, end: 22 },
  { start: 73, end: 77 },
  { start: 90, end: 94 },
  { start: 135, end: 139 },
  { start: 159, end: 163 },
];
