// Executed inside private Chrome. No fixture expectations are read here.
export function measureEditorial() {
  const rect = e => { const r=e.getBoundingClientRect(); return {x:r.x,y:r.y,right:r.right,bottom:r.bottom,width:r.width,height:r.height}; };
  const visible = e => { const r=e.getBoundingClientRect(); const s=getComputedStyle(e); return r.width>1 && r.height>1 && r.bottom>0 && r.top<innerHeight && s.visibility!=="hidden" && s.display!=="none"; };
  const buttons = [...document.querySelectorAll("button")].filter(visible).map(e => ({label:e.getAttribute("aria-label") ?? e.textContent.trim(), ...rect(e)}));
  const transcript = document.querySelector('[data-testid="transcript-virtual-list"]');
  const composer = document.querySelector('[data-testid="composer-input-card"]');
  const input = document.querySelector('textarea[aria-label="Message"]');
  const stamps = [...document.querySelectorAll('[data-testid="agent-message-item"]:not([data-opens-exchange="true"])')].flatMap(e => {
    const bubble=e.querySelector('[data-testid="agent-bubble"]');
    const stamp=e.lastElementChild;
    if (!bubble || !stamp || stamp===bubble) return [];
    return [{message:rect(e), bubble:rect(bubble), prose:rect(bubble.querySelector('p') ?? bubble), stamp:rect(stamp), position:getComputedStyle(stamp).position, fontStyle:getComputedStyle(stamp).fontStyle}];
  });
  const de=document.documentElement;
  return {viewport:{width:innerWidth,height:innerHeight}, document:{width:de.clientWidth,scrollWidth:de.scrollWidth,height:de.clientHeight,scrollHeight:de.scrollHeight},
    theme:de.dataset.theme,fontSize:document.body.dataset.fontSize, transcript:transcript?rect(transcript):null,
    composer:composer?rect(composer):null,input:input?{...rect(input),fontSize:getComputedStyle(input).fontSize}:null,
    buttons,stamps,prose:[...document.querySelectorAll('[data-testid="agent-bubble"]')].map(e=>({family:getComputedStyle(e).fontFamily,fontSize:getComputedStyle(e).fontSize})),
    lifecycle:[...document.querySelectorAll('[data-testid="delegate-lifecycle"]')].map(e=>({text:e.textContent,...rect(e)}))};
}

export function assertEditorialGeometry(assert, m, label) {
  assert(m.document.scrollWidth <= m.document.width + 1, `${label}: document horizontal overflow`);
  assert(m.document.scrollHeight <= m.document.height + 1, `${label}: document vertical overflow`);
  assert(m.composer && m.input && m.transcript, `${label}: real composer/transcript missing`);
  assert(m.composer.x >= -1 && m.composer.right <= m.viewport.width+1, `${label}: composer clipped`);
  assert(m.input.x >= m.composer.x-1 && m.input.right <= m.composer.right+1, `${label}: input escapes composer`);
  for (const b of m.buttons) assert(b.x>=-1 && b.right<=m.viewport.width+1, `${label}: horizontal control clipping: ${b.label}`);
  if (m.viewport.width<900) {
    assert(parseFloat(m.input.fontSize)>=16, `${label}: editable font below phone floor`);
    for (const b of m.buttons.filter(b=>b.label==="Send" || b.label==="Open")) assert(b.width>=44 && b.height>=44, `${label}: phone ${b.label} below 44px`);
  }
  for (const s of m.stamps) {
    if (m.transcript.width>=880) {
      assert.equal(s.position,"absolute",`${label}: missing timestamp rail`);
      assert(s.stamp.x>=s.bubble.right-1,`${label}: timestamp overlaps prose`);
      assert(Math.abs(s.stamp.y-s.prose.y)<=1,`${label}: timestamp drifts below first line`);
    } else {
      assert.equal(s.position,"static",`${label}: narrow timestamp left flow`);
      assert(s.stamp.right<=s.message.right+1 && s.stamp.y>=s.bubble.bottom-1,`${label}: narrow timestamp clips/overlaps`);
    }
    assert.equal(s.fontStyle,"italic",`${label}: timestamp lost italic treatment`);
  }
}
