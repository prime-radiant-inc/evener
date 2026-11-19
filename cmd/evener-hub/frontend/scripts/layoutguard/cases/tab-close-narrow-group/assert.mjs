// Verifies that a 200px Dockview group truncates a long tab title so the
// active tab's close action stays inside the group and hit-testable.
export default function assert(m) {
  const issues = [];
  if (!(m.closeWidth >= 16 && m.closeWidth <= 32)) {
    issues.push(`close action width ${m.closeWidth.toFixed(1)}px outside [16, 32]`);
  }
  if (!(m.closeLeft >= m.groupLeft && m.closeRight <= m.groupRight + 0.5)) {
    issues.push(
      `close action [${m.closeLeft.toFixed(1)}, ${m.closeRight.toFixed(1)}] escapes 200px group [${m.groupLeft.toFixed(1)}, ${m.groupRight.toFixed(1)}]`,
    );
  }
  if (!(m.tabRight <= m.groupRight + 0.5)) {
    issues.push(`tab right ${m.tabRight.toFixed(1)}px escapes group right ${m.groupRight.toFixed(1)}px`);
  }
  if (!(m.titleScrollWidth > m.titleClientWidth && m.titleClientWidth > 0)) {
    issues.push(`tab title did not truncate (scrollWidth=${m.titleScrollWidth}, clientWidth=${m.titleClientWidth})`);
  }
  if (!m.hitIsClose) {
    issues.push(`close center hit "${m.hitId}" instead of inspector-close`);
  }

  return {
    pass: issues.length === 0,
    reason:
      issues.length === 0
        ? `close [${m.closeLeft.toFixed(1)}, ${m.closeRight.toFixed(1)}] inside group [${m.groupLeft.toFixed(1)}, ${m.groupRight.toFixed(1)}], title truncated ${m.titleClientWidth}/${m.titleScrollWidth}px`
        : issues.join("; "),
  };
}
