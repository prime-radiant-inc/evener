const TOLERANCE = 1;

export default function assert(measurements) {
  const failures = [];
  for (const m of measurements) {
    const sessionDelta = Math.abs(m.sessionTitle - m.sessionContent);
    const spawnDelta = Math.abs(m.spawnTitle - m.spawnForm);
    const settingsDelta = Math.abs(m.settingsTitle - m.settingsNav);
    if (sessionDelta > TOLERANCE) {
      failures.push(
        `session header is ${sessionDelta.toFixed(1)}px off the transcript column at ${m.width}px (title ${m.sessionTitle.toFixed(1)}, content ${m.sessionContent.toFixed(1)})`,
      );
    }
    if (spawnDelta > TOLERANCE) {
      failures.push(
        `spawn header is ${spawnDelta.toFixed(1)}px off the form column at ${m.width}px (title ${m.spawnTitle.toFixed(1)}, form ${m.spawnForm.toFixed(1)})`,
      );
    }
    if (settingsDelta > TOLERANCE) {
      failures.push(
        `settings header is ${settingsDelta.toFixed(1)}px off the nav axis at ${m.width}px (title ${m.settingsTitle.toFixed(1)}, nav ${m.settingsNav.toFixed(1)})`,
      );
    }
  }
  return failures.length === 0
    ? {
        pass: true,
        reason: `session and spawn titles share their content column and settings shares the nav axis at ${measurements.map((m) => m.width).join("/")}px`,
      }
    : { pass: false, reason: failures.join("; ") };
}
