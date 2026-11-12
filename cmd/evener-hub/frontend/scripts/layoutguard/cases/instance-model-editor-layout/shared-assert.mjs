export default function assert(m) {
  const failures = [];

  if (!Number.isFinite(m.gap) || m.gap <= 0 || m.gap > 32) {
    failures.push(`invalid .fieldInline gap: ${m.gap}`);
  }
  if (!Number.isFinite(m.slack) || m.slack < -1 || m.slack > 2) {
    failures.push(
      `input does not stretch up to Reset inside .fieldInline (slack=${m.slack}px, inputWidth=${m.inputWidth}px, fieldInlineWidth=${m.fieldInlineWidth}px)`,
    );
  }
  if (
    !Number.isFinite(m.inputWidth) ||
    !Number.isFinite(m.fieldInlineWidth) ||
    m.inputWidth <= m.minInputWidth ||
    m.inputWidth > m.fieldInlineWidth
  ) {
    failures.push(
      `inputWidth ${m.inputWidth}px out of bounds (${m.minInputWidth}px..${m.fieldInlineWidth}px)`,
    );
  }
  if (!Number.isFinite(m.overflow) || m.overflow < -1 || m.overflow > 1) {
    failures.push(
      `.modelMeta right edge drifted from .modelRow right edge by ${m.overflow}px (expected within [-1, 1])`,
    );
  }
  if (!Number.isFinite(m.slotToMetaGap) || m.slotToMetaGap < -1 || m.slotToMetaGap > m.modelRowWidth) {
    failures.push(
      `.modelNameSlot collided with or escaped .modelMeta (slotToMetaGap=${m.slotToMetaGap}px)`,
    );
  }

  if (failures.length > 0) {
    return { pass: false, reason: failures.join("; ") };
  }
  return {
    pass: true,
    reason: `viewport ${m.viewport}px: inputWidth=${m.inputWidth.toFixed(1)}px (slack=${m.slack.toFixed(1)}px), modelRow overflow=${m.overflow.toFixed(1)}px`,
  };
}
