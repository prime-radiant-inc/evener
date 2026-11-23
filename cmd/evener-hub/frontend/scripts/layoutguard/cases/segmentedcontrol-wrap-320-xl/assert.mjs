// The design-system contract for the Overview category strip (design-system.md
// "Keep inspection readable at larger text sizes"): at a 320px sidebar with XL
// text, its labels and counts wrap instead of shortening. Geometry is the
// proof: every label and detail must contain its full text box (scrollWidth and
// scrollHeight within clientWidth/clientHeight), at least one of each must
// actually occupy two lines, and the track must grow in block size above the
// one-line floor those same options would occupy.
const TOLERANCE = 1;

export default function assert(measurement) {
  const failures = [];
  const m = measurement;
  const fail = (message) => failures.push(message);

  const finite = [
    ["sidebar.width", m.sidebar.width],
    ["track.width", m.track.width],
    ["track.height", m.track.height],
    ["track.scrollWidth", m.track.scrollWidth],
    ["track.clientWidth", m.track.clientWidth],
    ["oneLineLabelHeight", m.oneLineLabelHeight],
    ["oneLineDetailHeight", m.oneLineDetailHeight],
    ["stackedGap", m.stackedGap],
  ];
  for (const [name, value] of finite) {
    if (!Number.isFinite(value)) fail(`${name} is not a finite number (${value})`);
  }

  const blockChrome = (chrome) =>
    chrome.paddingTop + chrome.paddingBottom + chrome.borderTopWidth + chrome.borderBottomWidth;
  // The same five options if every label and count occupied one line.
  const oneLineContent = Number.isFinite(m.oneLineDetailHeight)
    ? m.oneLineLabelHeight + m.stackedGap + m.oneLineDetailHeight
    : m.oneLineLabelHeight;
  const oneLineTrackHeight = oneLineContent + blockChrome(m.optionChrome) + blockChrome(m.trackChrome);

  if (m.track.width > m.sidebar.width + TOLERANCE) {
    fail(`track is ${m.track.width.toFixed(1)}px wide inside a ${m.sidebar.width.toFixed(1)}px sidebar`);
  }
  if (m.track.left < m.sidebar.left - TOLERANCE || m.track.right > m.sidebar.right + TOLERANCE) {
    fail("track escapes the sidebar horizontally");
  }
  if (m.track.scrollWidth > m.track.clientWidth + TOLERANCE) {
    fail(`track content is ${m.track.scrollWidth}px wide in a ${m.track.clientWidth}px track`);
  }

  let wrappedLabels = 0;
  let wrappedDetails = 0;

  const checkText = (text, name) => {
    for (const [property, value] of [
      ["width", text.width],
      ["height", text.height],
      ["clientWidth", text.clientWidth],
      ["clientHeight", text.clientHeight],
      ["scrollWidth", text.scrollWidth],
      ["scrollHeight", text.scrollHeight],
      ["lineHeight", text.lineHeight],
    ]) {
      if (!Number.isFinite(value)) {
        fail(`${name}.${property} is not a finite number (${value})`);
        return;
      }
    }
    if (text.clientWidth <= 0 || text.clientHeight <= 0) fail(`${name} has no content box`);
    if (text.scrollWidth > text.clientWidth + TOLERANCE) {
      fail(`${name} is ellipsized: ${text.scrollWidth}px of text in a ${text.clientWidth}px box`);
    }
    if (text.scrollHeight > text.clientHeight + TOLERANCE) {
      fail(`${name} is clipped vertically: ${text.scrollHeight}px of text in a ${text.clientHeight}px box`);
    }
    if (text.whiteSpace !== "normal") fail(`${name} keeps white-space:${text.whiteSpace}, so it cannot wrap`);
    if (text.overflowWrap !== "anywhere")
      fail(`${name} keeps overflow-wrap:${text.overflowWrap}, so long tokens cannot break`);
    if (text.textOverflow === "ellipsis") fail(`${name} still declares text-overflow:ellipsis`);
    if (text.overflow === "hidden") fail(`${name} still clips overflow`);
  };

  m.options.forEach((option, index) => {
    const name = `option ${index + 1}`;
    if (
      !Number.isFinite(option.option.left) ||
      option.option.left < m.track.left - TOLERANCE ||
      option.option.right > m.track.right + TOLERANCE ||
      option.option.top < m.track.top - TOLERANCE ||
      option.option.bottom > m.track.bottom + TOLERANCE
    ) {
      fail(`${name} escapes the track`);
    }
    checkText(option.label, `${name} label`);
    if (option.label.height > option.label.lineHeight * 1.5) wrappedLabels += 1;
    if (option.detail !== null) {
      checkText(option.detail, `${name} detail`);
      if (option.detail.height > option.detail.lineHeight * 1.5) wrappedDetails += 1;
    }
  });

  if (wrappedLabels === 0) fail("no label wrapped, so the fixture does not exercise the wrap layout");
  if (wrappedDetails === 0) fail("no count wrapped, so the fixture does not exercise the wrap layout");
  if (!(m.track.height > oneLineTrackHeight + TOLERANCE)) {
    fail(
      `track is ${m.track.height.toFixed(1)}px tall, not above its ${oneLineTrackHeight.toFixed(1)}px one-line floor`,
    );
  }

  return failures.length === 0
    ? {
        pass: true,
        reason: `320px XL text: ${wrappedLabels} label(s) and ${wrappedDetails} count(s) wrap in full inside their columns, and the track grows to ${m.track.height.toFixed(1)}px above the ${oneLineTrackHeight.toFixed(1)}px one-line floor`,
      }
    : { pass: false, reason: failures.join("; ") };
}
