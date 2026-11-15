export default function assert(m) {
  const failures = [];

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
  if (!Number.isFinite(m.labelOverlap) || m.labelOverlap > 0) {
    failures.push(
      `model label spilled into .modelMeta by ${m.labelOverlap}px instead of wrapping`,
    );
  }

  if (failures.length > 0) {
    return { pass: false, reason: failures.join("; ") };
  }
  return {
    pass: true,
    reason: `viewport ${m.viewport}px: modelRow overflow=${m.overflow.toFixed(1)}px, slotToMetaGap=${m.slotToMetaGap.toFixed(1)}px`,
  };
}
