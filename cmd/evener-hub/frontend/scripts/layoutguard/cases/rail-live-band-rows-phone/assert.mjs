import assertLines from "../rail-live-band-rows/shared-assert.mjs";

export default function assert(m) {
  const { title, quiet, band, age, why } = m.type;
  const namesLead = Math.min(title.size, quiet.size) > Math.max(band.size, age.size, why.size)
    && Math.min(title.weight, quiet.weight) > Math.max(age.weight, why.weight);
  if (!namesLead) {
    return { pass: false, reason: `session names do not lead their group and metadata: ${JSON.stringify(m.type)}` };
  }
  if (!(m.textStep > why.size / 2 && m.textStep <= m.titleLineHeight + 1)) {
    return { pass: false, reason: `name and reason are ${m.textStep}px apart for a ${m.titleLineHeight}px line` };
  }
  const lines = assertLines(m);
  return lines.pass ? { pass: true, reason: `text lines ${m.textStep}px apart; ${lines.reason}` } : lines;
}
