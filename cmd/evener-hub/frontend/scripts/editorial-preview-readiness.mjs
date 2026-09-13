import { measureEditorial } from "./editorial-preview-measure.mjs";

// Executed inside private Chrome; reads real layout/hit targets only.
export function measureDesktopCollaborators(identity = {nodes: []}) {
  const rect = element => {
    const r = element.getBoundingClientRect();
    return {x:r.x,y:r.y,right:r.right,bottom:r.bottom,width:r.width,height:r.height};
  };
  const list = document.querySelector('[data-testid="transcript-virtual-list"]').firstElementChild;
  const viewport = rect(list);
  const target = element => {
    if (!identity.nodes.includes(element)) identity.nodes.push(element);
    const box = rect(element);
    const style = getComputedStyle(element);
    const x = box.x + box.width / 2, y = box.y + box.height / 2;
    const hit = document.elementFromPoint(x, y);
    const contained = box.width > 0 && box.height > 0 && box.x >= viewport.x && box.right <= viewport.right && box.y >= viewport.y && box.bottom <= viewport.bottom;
    return {id:identity.nodes.indexOf(element),row:element.closest('[data-row-id]')?.dataset.rowId,
      text:element.getAttribute('aria-label') ?? element.textContent.trim(),connected:element.isConnected,rect:box,
      contained,hit:!!hit && (hit === element || element.contains(hit)),
      visible:style.visibility !== 'hidden' && style.display !== 'none',
      hitElement:hit ? {tag:hit.tagName,text:hit.textContent.slice(0,100)} : null};
  };
  const lifecycle = [...document.querySelectorAll('[data-testid="delegate-lifecycle"]')].map(target);
  const opens = [...list.querySelectorAll('[aria-label="Open transcript"]')].map(target);
  const ready = lifecycle.length === 3 && opens.length > 0 && [...lifecycle,...opens].every(t => t.connected && t.contained && t.visible && t.hit);
  return {at:performance.now(),scrollTop:list.scrollTop,scrollHeight:list.scrollHeight,clientHeight:list.clientHeight,viewport,lifecycle,opens,ready};
}

export function assertDesktopCollaborators(assert, sample, label) {
  assert.equal(sample.lifecycle.length, 3, `${label}: desktop collaborator states missing`);
  assert(sample.opens.length > 0, `${label}: desktop Open targets missing`);
  for (const target of [...sample.lifecycle, ...sample.opens]) {
    assert(target.connected && target.contained && target.visible && target.hit, `${label}: desktop collaborator target not inspectable: ${target.text}`);
  }
}

export async function measureReadyDesktopCollaborators(evaluate) {
  return evaluate(`(async () => {
    const identity={nodes:[]};
    const probe=()=>(${measureDesktopCollaborators.toString()})(identity);
    const trace=[{stage:'before-readiness',...probe()}];
    const start=performance.now();
    await new Promise((resolve,reject)=>{
      const sample=()=>{
        const current=probe(); trace.push({stage:'readiness-frame',...current});
        if(current.ready) resolve();
        else if(performance.now()-start>15000) reject(new Error('Desktop collaborator readiness timeout: '+JSON.stringify(trace)));
        else requestAnimationFrame(sample);
      };
      requestAnimationFrame(sample);
    });
    // Same browser task: a later screenshot must not stand in for this sample.
    return {trace,readiness:probe(),geometry:(${measureEditorial.toString()})()};
  })()`);
}
