// The hierarchy list exists to show the whole ancestry: each ancestor's title
// (both fixtures are several sidebar-widths long, one a single unbroken path)
// must wrap onto more than one line with nothing cut off, and stay inside the
// list's width.
const TOLERANCE = 1;

export default function assert(m) {
  const failures = [];
  m.ancestors.forEach((a, index) => {
    const name = `ancestor ${index + 1}`;
    if (a.scrollWidth > a.clientWidth + TOLERANCE) {
      failures.push(`${name} is cut off: ${a.scrollWidth}px of text in a ${a.clientWidth}px box`);
    }
    if (a.rect.height < a.lineHeight * 2 - TOLERANCE) {
      failures.push(`${name} stays on one ${a.rect.height.toFixed(1)}px line instead of wrapping`);
    }
    if (a.rect.right > m.nav.right + TOLERANCE) {
      failures.push(`${name} spills ${(a.rect.right - m.nav.right).toFixed(1)}px past the list`);
    }
  });
  return failures.length === 0
    ? {
        pass: true,
        reason: `every ancestor wraps in full inside the list (${m.ancestors.map((a) => `${Math.round(a.rect.height / a.lineHeight)} lines`).join(", ")})`,
      }
    : { pass: false, reason: failures.join("; ") };
}
