import assertLines from "../rail-live-band-rows/shared-assert.mjs";

export default function assert(m) {
  const { title, quiet, band, age, why } = m.type;
  const namesLead = Math.min(title.size, quiet.size) > band.size
    && Math.min(title.weight, quiet.weight) > Math.max(age.weight, why.weight);
  if (!namesLead) {
    return { pass: false, reason: `session names do not lead their group and metadata: ${JSON.stringify(m.type)}` };
  }
  return assertLines(m);
}
