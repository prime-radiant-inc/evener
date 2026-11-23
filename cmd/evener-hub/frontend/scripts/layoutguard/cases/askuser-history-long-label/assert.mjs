export default function assert(m) {
  const failures = [];
  const cardRight = m.card.right;
  for (const [index, line] of m.labelLines.entries()) {
    if (line.right > cardRight + 1) {
      failures.push(`label line ${index + 1} escapes ${(line.right - cardRight).toFixed(1)}px past the card`);
    }
  }
  if (m.labelLines.length < 2) {
    failures.push("the unbroken label stayed on one line instead of wrapping");
  }
  if (m.label.width > m.card.width + 1) {
    failures.push(`label box is ${m.label.width}px wide inside a ${m.card.width}px card`);
  }
  return failures.length === 0
    ? { pass: true, reason: "read-only unbroken option label wraps inside the historical card" }
    : { pass: false, reason: failures.join("; ") };
}
