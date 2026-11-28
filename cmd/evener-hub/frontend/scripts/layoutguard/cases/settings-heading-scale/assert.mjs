// Geometric contract for the settings heading recipes in surface.module.css:
//
//   A. A section heading takes the pane-title size, the size a settings pane's
//      own title takes, rather than the user-agent h2 scale.
//   B. A subsection heading takes the body size, one step down from A.
//   C. Both carry no vertical margin: the section's own stack owns the gaps.
// The probes declare only the sizes the tokens name, so a changed token moves
// both sides together and only a changed heading recipe can fail the case.
export default function assert({ section, subsection, paneTitleProbe, bodyProbe }) {
  const failures = [];
  const same = (a, b) => Math.abs(a - b) <= 0.01;
  const margins = (heading) => heading.marginTop === 0 && heading.marginBottom === 0;

  if (!same(section.fontSize, paneTitleProbe.fontSize)) {
    failures.push(`section heading font-size is ${section.fontSize}px, expected the pane-title probe ${paneTitleProbe.fontSize}px`);
  }
  if (!same(subsection.fontSize, bodyProbe.fontSize)) {
    failures.push(`subsection heading font-size is ${subsection.fontSize}px, expected the body probe ${bodyProbe.fontSize}px`);
  }
  if (!margins(section)) {
    failures.push(`section heading margins are ${section.marginTop}px/${section.marginBottom}px, expected 0/0`);
  }
  if (!margins(subsection)) {
    failures.push(`subsection heading margins are ${subsection.marginTop}px/${subsection.marginBottom}px, expected 0/0`);
  }

  return failures.length === 0
    ? { pass: true, reason: "settings headings keep the pane-title/body sizes and zero margins" }
    : { pass: false, reason: failures.join("; ") };
}
