import { mkdir, readdir, writeFile } from "node:fs/promises";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { preview } from "vite";
import { evaluate, navigate } from "./browser/cdp.mjs";
import { startChrome, withOwnedCleanup } from "./browser/chrome.mjs";
import {
  analyzeDifferentialCapture,
  applyRenderedSamples,
  assertGeometry,
  extractPaintStack,
  measurePage,
  mergeStabilizationEvidence,
  selectFocusIndicator,
  stabilizePagePaint,
} from "./browser/geometry.mjs";

const concepts = ["stillwater", "constellation", "field-notes"];
const conceptNames = {
  stillwater: "Stillwater",
  constellation: "Constellation",
  "field-notes": "Field Notes",
};
const viewports = {
  iosPortrait: { width: 393, height: 852, platform: "ios" },
  androidPortrait: { width: 412, height: 915, platform: "android" },
  landscape: { width: 852, height: 393, platform: "ios" },
};
const agents = {
  ios: {
    userAgent:
      "Mozilla/5.0 (iPhone; CPU iPhone OS 18_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.6 Mobile/15E148 Safari/604.1",
    platform: "iPhone",
    touch: 5,
  },
  android: {
    userAgent:
      "Mozilla/5.0 (Linux; Android 16; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Mobile Safari/537.36",
    platform: "Linux armv8l",
    touch: 5,
  },
};
const sentinelSource = `(() => {
  const attempts = window.__capabilityAttempts = [];
  const trap = name => function(){ attempts.push(name); throw new Error('forbidden runtime capability attempted: ' + name); };
  const set = (target, key, value) => { try { Object.defineProperty(target, key, { configurable: true, writable: true, value }); } catch (error) { attempts.push('sentinel-install:' + key); throw error; } };
  for (const name of ['fetch','XMLHttpRequest','WebSocket','EventSource','SpeechRecognition','webkitSpeechRecognition','SpeechSynthesisUtterance','AudioContext','webkitAudioContext','showOpenFilePicker','showSaveFilePicker','showDirectoryPicker']) set(window, name, trap(name));
  set(navigator, 'sendBeacon', trap('navigator.sendBeacon'));
  set(navigator, 'mediaDevices', { getUserMedia: trap('navigator.mediaDevices.getUserMedia') });
  set(window, 'speechSynthesis', Object.fromEntries(['speak','cancel','pause','resume','getVoices'].map(k => [k, trap('speechSynthesis.' + k)])));
  set(navigator, 'geolocation', Object.fromEntries(['getCurrentPosition','watchPosition','clearWatch'].map(k => [k, trap('geolocation.' + k)])));
  set(navigator, 'vibrate', trap('navigator.vibrate'));
  const NotificationTrap = trap('Notification'); NotificationTrap.requestPermission = trap('Notification.requestPermission'); set(window, 'Notification', NotificationTrap);
  const sw = Object.fromEntries(['register','getRegistration','getRegistrations'].map(k => [k, trap('serviceWorker.' + k)])); Object.defineProperty(sw, 'ready', { get: trap('serviceWorker.ready') }); set(navigator, 'serviceWorker', sw);
  for (const [name, methods] of Object.entries({ServiceWorkerRegistration:['showNotification','getNotifications'],PushManager:['subscribe','getSubscription','permissionState'],PushSubscription:['unsubscribe']})) { const C = trap(name); for (const method of methods) C.prototype[method] = trap(name + '.' + method); set(window, name, C); }
})()`;

async function stabilizePaint(client) {
  return evaluate(
    client,
    `(${stabilizePagePaint.toString()})(document,()=>new Promise(resolve=>requestAnimationFrame(()=>resolve())))`,
  );
}

export function assembleCaseStabilization(
  preCollection,
  focusSteps,
  postFocus,
) {
  return mergeStabilizationEvidence({
    preCollection,
    focusSteps,
    postFocus,
  });
}

function actionSource(concept, destination, textScale, variant = {}) {
  return `(async () => {
    const frame = () => new Promise(resolve => requestAnimationFrame(() => resolve()));
    const controls = () => [...document.querySelectorAll('button,input,textarea,select,a[href]')];
    const waitFor = selector => new Promise(resolve => { const current=document.querySelector(selector); if(current){resolve(current);return} const observer=new MutationObserver(()=>{const found=document.querySelector(selector);if(found){observer.disconnect();resolve(found)}});observer.observe(document,{subtree:true,childList:true,attributes:true}); });
    const named = name => controls().find(el => (el.getAttribute('aria-label') || el.textContent || '').trim() === name);
    const click = async name => { const el = named(name); if (!el) throw new Error('missing real control: ' + name); el.click(); await frame(); await frame(); return el; };
    const selectorClick = async selector => { const holder = document.querySelector(selector); const el = holder?.matches('button,a,input') ? holder : holder?.querySelector('button,a,input'); if (!el) throw new Error('missing real control: ' + selector); el.click(); await frame(); await frame(); return {holder,el}; };
    const input = async (label, value) => { const el = controls().find(node => node.getAttribute('aria-label') === label); if (!el) throw new Error('missing real field: ' + label); const setter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), 'value')?.set; setter.call(el, value); el.dispatchEvent(new Event('input', {bubbles:true})); el.dispatchEvent(new Event('change', {bubbles:true})); await frame(); };
    await frame(); await frame();
    if (!document.querySelector('[data-testid="concept-gallery"]')) throw new Error('case storage was not isolated');
    await click(${JSON.stringify(`Select ${conceptNames[concept]}`)});
    await waitFor('main[data-route="sessions"]');
    if (${JSON.stringify(variant.scenario ?? null)}) { await click('Lab Controls'); const scenario = [...document.querySelectorAll('input[name="scenario"]')].find(el => el.parentElement?.textContent?.trim() === ${JSON.stringify(variant.scenario ?? "")}); if (!scenario) throw new Error('missing scenario control'); scenario.click(); await frame(); if (named('Close Lab Controls')) await click('Close Lab Controls'); }
    if (${JSON.stringify(variant.appearance ?? null)}) { await click('Lab Controls'); const appearance = [...document.querySelectorAll('input[name="appearance"]')].find(el=>el.parentElement?.textContent?.trim()===${JSON.stringify(variant.appearance ?? "")}); if (!appearance) throw new Error('missing Appearance'); appearance.click(); await frame(); await frame(); await click('Close Lab Controls'); }
    if (${JSON.stringify(Boolean(variant.reducedMotion))}) { await click('Lab Controls'); const reduced = document.querySelector('.lab-controls__check input[type="checkbox"]'); if (!reduced) throw new Error('missing reduced motion control'); reduced.click(); await frame(); await frame(); if(!reduced.checked)throw new Error('reduced motion checkbox did not check'); await click('Close Lab Controls'); }
    if (${JSON.stringify(destination)} === 'search') { await click('Search'); await input('Search', 'Fixture-first plan'); if(!document.querySelector('[data-search-result-id]'))throw new Error('search result state not rendered'); }
    if (['conversation','work','voice','voice-speaking'].includes(${JSON.stringify(destination)})) { await selectorClick('[data-session-id="session-native-client"]'); }
    if (${JSON.stringify(destination)} === 'question') await selectorClick('[data-session-id="session-mobile-release"]');
    if (${JSON.stringify(destination)} === 'conversation') { const tool=await click('Inspect fixture schema'); if(tool.getAttribute('aria-expanded')!=='true'&&!document.querySelector('[data-long-tool-output]'))throw new Error('tool disclosure did not expand'); }
    if (${JSON.stringify(destination)} === 'work') { await click('Work'); const work=await selectorClick('[data-work-node-id]'); if(work.el.getAttribute('aria-expanded')!=='true'&&work.holder.getAttribute('aria-expanded')!=='true'&&!work.holder.querySelector('[aria-expanded="true"]'))throw new Error('subagent disclosure did not expand'); }
    if (${JSON.stringify(destination)} === 'question') { const option = await waitFor('[data-question-id] input'); option.click(); await frame(); await input('Note', 'Browser matrix note'); if(!option.checked||document.querySelector('textarea[aria-label="Note"]')?.value!=='Browser matrix note')throw new Error('question selection/note did not persist'); }
    if (${JSON.stringify(destination)} === 'new') { await click('New Session'); await selectorClick('[data-project-id]'); await input('Prompt', 'Browser matrix populated prompt'); if(!document.querySelector('[aria-label="Project path"]')?.value||document.querySelector('[aria-label="Prompt"]')?.value!=='Browser matrix populated prompt')throw new Error('New Session fields not populated'); }
    if (${JSON.stringify(destination)} === 'settings') { await click('Settings'); await click('Lab Controls'); }
    if (${JSON.stringify(destination)}.startsWith('voice')) { await click('Voice'); await click('Set voice state: listening'); ${destination === "voice-speaking" ? "await click('Set voice state: speaking');" : ""} const expected=${JSON.stringify(destination === "voice-speaking" ? "speaking" : "listening")}; if(document.querySelector('[aria-label="Set voice state: '+expected+'"]')?.getAttribute('aria-pressed')!=='true')throw new Error('voice state did not reach '+expected); }
    if (${JSON.stringify(textScale)} === 'accessibility') { if (!document.querySelector('[role="dialog"]')) await click('Lab Controls'); const radio = document.querySelector('input[name="text-scale"][value="accessibility"]') || [...document.querySelectorAll('input[type="radio"]')].find(el => el.parentElement?.textContent?.trim() === 'accessibility'); if (!radio) throw new Error('missing accessibility text control'); radio.click(); await frame(); if (${JSON.stringify(destination)} !== 'settings') await click('Close Lab Controls'); }
    const preferences=JSON.parse(localStorage.getItem('evener-concepts.preferences.v1')||'{}');
    if(${JSON.stringify(variant.scenario ?? null)}&&preferences.scenario!==${JSON.stringify(variant.scenario ?? null)})throw new Error('scenario state did not persist');
    if(${JSON.stringify(["loading", "empty", "error"].includes(variant.scenario))}&&!document.querySelector('[data-screen-state=${variant.scenario ?? ""}]'))throw new Error('scenario screen state not rendered');
    if(${JSON.stringify(variant.scenario === "offline")}&&!document.querySelector('[data-offline-policy="read-only"]'))throw new Error('offline policy state not rendered');
    if(${JSON.stringify(variant.appearance ?? null)}&&document.documentElement.dataset.appearance!==${JSON.stringify(variant.appearance ?? null)})throw new Error('appearance state did not apply');
    if(${JSON.stringify(Boolean(variant.reducedMotion))}&&(preferences.reducedMotion!==true||document.documentElement.dataset.reducedMotion!=='true'))throw new Error('reduced motion state did not apply: '+JSON.stringify({preference:preferences.reducedMotion,resolved:document.documentElement.dataset.reducedMotion}));
    if(${JSON.stringify(destination)}==='sessions'&&${JSON.stringify(!variant.scenario)})for(const group of ['needs-you','running','recent'])if(!document.querySelector('[data-session-group-id="'+group+'"]'))throw new Error('Sessions missing '+group+' group');
    return { route: document.querySelector('main')?.dataset.route, platform: document.documentElement.dataset.platform, appearance:document.documentElement.dataset.appearance,reducedMotion:document.documentElement.dataset.reducedMotion,scenario:preferences.scenario,fileInputs: document.querySelectorAll('input[type="file"],input[capture]').length, attempts: window.__capabilityAttempts };
  })()`;
}

async function focusAudit(client) {
  const stabilization = await stabilizePaint(client);
  const setup = await evaluate(
    client,
    `(() => {
    const candidates=[...document.querySelectorAll('button:not([disabled]),input:not([disabled]):not([type="hidden"]),textarea:not([disabled]),select:not([disabled]),a[href],[tabindex]:not([tabindex="-1"])')].filter(e=>{const r=e.getBoundingClientRect(),s=getComputedStyle(e),radioSkipped=e.matches('input[type="radio"]')&&!e.checked&&document.querySelector('input[type="radio"][name="'+CSS.escape(e.name)+'"]:checked');return r.width>0&&r.height>0&&s.visibility!=='hidden'&&!e.closest('[inert],[aria-hidden="true"]')&&!radioSkipped});
    const style=e=>{const s=getComputedStyle(e);return{outlineStyle:s.outlineStyle,outlineWidth:s.outlineWidth,outlineColor:s.outlineColor,outlineOffset:s.outlineOffset,boxShadow:s.boxShadow};};
    const name=(e,i)=>e.id||e.getAttribute('aria-label')||e.textContent?.trim().slice(0,80)||e.tagName+'-'+i;
    const expected=candidates.map((e,i)=>{e.dataset.browserFocusId='focus-'+i;return{id:e.dataset.browserFocusId,subject:name(e,i),before:style(e)};});
    document.body.tabIndex=-1;document.body.focus();document.body.removeAttribute('tabindex');
    return expected;
  })()`,
  );
  const expected = setup.map(({ id }) => id);
  const actual = [];
  const stabilizations = [stabilization];
  for (let index = 0; index < expected.length; index += 1) {
    await client.send("Input.dispatchKeyEvent", {
      type: "keyDown",
      key: "Tab",
      code: "Tab",
      windowsVirtualKeyCode: 9,
    });
    await client.send("Input.dispatchKeyEvent", {
      type: "keyUp",
      key: "Tab",
      code: "Tab",
      windowsVirtualKeyCode: 9,
    });
    stabilizations.push(await stabilizePaint(client));
    actual.push(
      await evaluate(
        client,
        `(() => {
      const selectFocusIndicator=${selectFocusIndicator.toString()};
      const extractPaintStack=${extractPaintStack.toString()};
      const e=document.activeElement,s=getComputedStyle(e),before=${JSON.stringify(setup)}.find(item=>item.id===e.dataset.browserFocusId)?.before;
      const after={outlineStyle:s.outlineStyle,outlineWidth:s.outlineWidth,outlineColor:s.outlineColor,outlineOffset:s.outlineOffset,boxShadow:s.boxShadow};
      const indicator=selectFocusIndicator(before,after),subject=${JSON.stringify(setup)}.find(item=>item.id===e.dataset.browserFocusId)?.subject||e.tagName;
      const outside=extractPaintStack(e.parentElement,(target,pseudo)=>getComputedStyle(target,pseudo));
      const inside=extractPaintStack(e,(target,pseudo)=>getComputedStyle(target,pseudo));
      const box=e.getBoundingClientRect(),geometry={left:box.left,right:box.right,top:box.top,bottom:box.bottom,width:box.width,height:box.height};
      const candidates=indicator.changed?[{id:subject+' focus indicator',kind:'focus',foreground:indicator.paints[0]?.color??'transparent',backgrounds:[{color:'transparent',image:'none',opacity:Number(s.opacity),owner:subject},...outside.layers],insideBackgrounds:inside.layers,minimum:3,changed:true,before,after,source:'rendered-focus-difference',candidateOpacity:Number(s.opacity),ancestorOpacities:outside.layers.map(layer=>layer.opacity),geometry,paint:{source:'rendered-focus-difference'},oracleId:e.dataset.browserFocusId,geometryCollectedAt:performance.now()}]:[];
      if(!indicator.changed)candidates.push({id:subject,kind:'focus',foreground:'transparent',backgrounds:outside.layers,minimum:3,changed:false,before,after,source:'none'});
      return{id:e.dataset.browserFocusId||'',subject,visible:indicator.changed,candidates};
    })()`,
      ),
    );
  }
  if (expected.length > 1) {
    await client.send("Input.dispatchKeyEvent", {
      type: "keyDown",
      key: "Tab",
      code: "Tab",
      modifiers: 8,
      windowsVirtualKeyCode: 9,
    });
    await client.send("Input.dispatchKeyEvent", {
      type: "keyUp",
      key: "Tab",
      code: "Tab",
      modifiers: 8,
      windowsVirtualKeyCode: 9,
    });
    const reverse = await evaluate(
      client,
      "document.activeElement.dataset.browserFocusId || ''",
    );
    if (reverse !== actual.at(-2)?.id)
      throw new Error(
        `Shift+Tab mismatch: expected ${actual.at(-2)?.id}, got ${reverse}`,
      );
  }
  return {
    focusCandidates: actual.flatMap(({ candidates }) => candidates),
    focusOrder: actual.map((item, index) => ({
      id: item.subject,
      order: expected.indexOf(item.id) + 1,
      visible: item.visible,
      actualOrder: index + 1,
    })),
    stabilization,
    stabilizations,
  };
}

async function setupPage(client, origin, viewport, safeArea, sentinels = true) {
  const ua = agents[viewport.platform];
  const layoutHeight = viewport.layoutHeight ?? viewport.height;
  await Promise.all([
    client.send("Page.enable"),
    client.send("Runtime.enable"),
    client.send("Network.enable"),
  ]);
  await client.send("Storage.clearDataForOrigin", {
    origin,
    storageTypes: "all",
  });
  const metrics = {
    width: viewport.width,
    height: layoutHeight,
    deviceScaleFactor: 2,
    mobile: true,
    screenWidth: viewport.width,
    screenHeight: layoutHeight,
  };
  if (viewport.visibleHeight)
    metrics.viewport = {
      x: 0,
      y: 0,
      width: viewport.width,
      height: viewport.visibleHeight,
      scale: 1,
    };
  await client.send("Emulation.setDeviceMetricsOverride", metrics);
  if (viewport.visibleHeight)
    await client.send("Emulation.setVisibleSize", {
      width: viewport.width,
      height: viewport.visibleHeight,
    });
  await client.send("Emulation.setUserAgentOverride", {
    userAgent: ua.userAgent,
    platform: ua.platform,
  });
  await client.send("Emulation.setSafeAreaInsetsOverride", {
    insets: {
      top: safeArea.top,
      right: safeArea.right,
      bottom: safeArea.bottom,
      left: safeArea.left,
    },
  });
  await client.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `localStorage.setItem('evener-concepts.preferences.v1','{"version":1,"concept":null,"appearance":"system","textScale":"standard","reducedMotion":false,"scenario":"baseline"}');Object.defineProperty(navigator,'maxTouchPoints',{configurable:true,get:()=>${ua.touch}});${sentinels ? sentinelSource : ""}`,
  });
}

async function screenshot(client, file) {
  const image = await client.send("Page.captureScreenshot", {
    format: "png",
    fromSurface: true,
    captureBeyondViewport: false,
  });
  await writeFile(file, Buffer.from(image.data, "base64"));
  return image.data;
}

async function paintFrames(client) {
  await evaluate(
    client,
    "new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(()=>resolve(true))))",
  );
}

async function fullPageCapture(client) {
  const metrics = await client.send("Page.getLayoutMetrics");
  const content = metrics.cssContentSize ?? metrics.contentSize;
  const clip = {
    x: content.x,
    y: content.y,
    width: content.width,
    height: content.height,
    scale: 1,
  };
  const state = await evaluate(
    client,
    "({scroll:{x:scrollX,y:scrollY},devicePixelRatio,viewport:{width:innerWidth,height:innerHeight},capturedAt:performance.now()})",
  );
  const image = await client.send("Page.captureScreenshot", {
    format: "png",
    fromSurface: true,
    captureBeyondViewport: true,
    clip,
  });
  return {
    data: image.data,
    metadata: {
      coordinateSystem: "document-css-pixels",
      clip,
      scroll: state.scroll,
      devicePixelRatio: state.devicePixelRatio,
      viewport: state.viewport,
      capturedAt: state.capturedAt,
    },
  };
}

async function installGlobalProbe(client, kind, state, oracleIds) {
  await evaluate(
    client,
    `(() => {
      let style=document.getElementById('browser-contrast-probe');
      if(!style){style=document.createElement('style');style.id='browser-contrast-probe';document.head.append(style)}
      const kind=${JSON.stringify(kind)},state=${JSON.stringify(state)},oracleIds=${JSON.stringify(oracleIds)};
      const color=state==='black'?'rgb(0 0 0)':state==='white'?'rgb(255 255 255)':'transparent';
      const attribute=kind==='text'?'data-browser-text-candidate':'data-browser-nontext-candidate';
      const selectors=oracleIds.map(id=>'['+attribute+'="'+CSS.escape(id)+'"]');
      if(kind==='text'){const joined=selectors.join(',');style.textContent=joined+'{color:'+color+'!important;-webkit-text-fill-color:'+color+'!important;text-shadow:none!important}'+selectors.map(selector=>selector+'::placeholder').join(',')+'{color:'+color+'!important;-webkit-text-fill-color:'+color+'!important;opacity:1!important}'}
      else {const fill=selectors.map(selector=>selector+'[data-browser-nontext-source="fill"]').join(','),border=selectors.map(selector=>selector+'[data-browser-nontext-source="border"]').join(',');style.textContent=fill+'{background-color:'+color+'!important;background-image:none!important}'+border+'{border-color:'+color+'!important;border-image:none!important}'}
      return true;
    })()`,
  );
  await paintFrames(client);
}

async function removeGlobalProbe(client) {
  await evaluate(
    client,
    "document.getElementById('browser-contrast-probe')?.remove();true",
  );
  await paintFrames(client);
}

async function decodeAndAnalyze(client, candidates, captures) {
  return evaluate(
    client,
    `(async()=>{
      const decode=source=>new Promise((resolve,reject)=>{const image=new Image();image.onload=()=>{const canvas=document.createElement('canvas');canvas.width=image.naturalWidth;canvas.height=image.naturalHeight;const context=canvas.getContext('2d',{willReadFrequently:true});context.drawImage(image,0,0);resolve({width:canvas.width,height:canvas.height,data:context.getImageData(0,0,canvas.width,canvas.height).data})};image.onerror=()=>reject(new Error('contrast screenshot decode failed'));image.src='data:image/png;base64,'+source});
      const names=['original','hidden','black','white'],decoded={};
      for(const name of names)decoded[name]=await decode(${JSON.stringify(captures)}[name].data);
      const metadata=${JSON.stringify(captures.original.metadata)};
      metadata.timing={sequence:['stabilized','geometry','original','hidden','black-probe','white-probe'],eventDriven:true,captures:{original:${JSON.stringify(captures.original.metadata.capturedAt)},hidden:${JSON.stringify(captures.hidden.metadata.capturedAt)},blackProbe:${JSON.stringify(captures.black.metadata.capturedAt)},whiteProbe:${JSON.stringify(captures.white.metadata.capturedAt)}}};
      const analyze=${analyzeDifferentialCapture.toString()};
      return ${JSON.stringify(candidates)}.map(candidate=>analyze(candidate,{...metadata,images:decoded}));
    })()`,
  );
}

async function captureGlobalCandidates(client, candidates) {
  const results = new Map();
  for (const kind of ["text", "nontext"]) {
    const selected = candidates.filter((candidate) => candidate.kind === kind);
    if (selected.length === 0) continue;
    const batches = [];
    const overlaps = (left, right) =>
      left.left < right.right &&
      left.right > right.left &&
      left.top < right.bottom &&
      left.bottom > right.top;
    for (const candidate of selected) {
      const batch = batches.find((values) =>
        values.every((value) => !overlaps(value.geometry, candidate.geometry)),
      );
      if (batch) batch.push(candidate);
      else batches.push([candidate]);
    }
    for (const batch of batches) {
      const captures = { original: await fullPageCapture(client) };
      try {
        for (const state of ["hidden", "black", "white"]) {
          await installGlobalProbe(
            client,
            kind,
            state,
            batch.map(({ raw }) => raw.oracleId),
          );
          captures[state] = await fullPageCapture(client);
        }
      } finally {
        await removeGlobalProbe(client);
      }
      const evidence = await decodeAndAnalyze(client, batch, captures);
      batch.forEach((candidate, index) =>
        results.set(candidate.index, evidence[index]),
      );
    }
  }
  return results;
}

function focusProbeSource(candidate, state) {
  return `(() => {
    const target=document.querySelector('[data-browser-focus-id=${JSON.stringify(candidate.raw.oracleId)}]');
    if(!target)throw new Error('missing focus oracle target: '+${JSON.stringify(candidate.raw.oracleId)});
    const before=${JSON.stringify(candidate.raw.before)},after=${JSON.stringify(candidate.raw.after)},state=${JSON.stringify(state)};
    const split=value=>{if(!value||value==='none')return[];const parts=[];let depth=0,start=0;for(let index=0;index<value.length;index+=1){const char=value[index];if(char==='(')depth+=1;else if(char===')')depth-=1;else if(char===','&&depth===0){parts.push(value.slice(start,index).trim());start=index+1}}parts.push(value.slice(start).trim());return parts.filter(Boolean)};
    const recolor=(shadow,color)=>shadow.replace(/(?:rgba?\\([^)]*\\)|#[0-9a-fA-F]{3,8}|\\b(?:black|white|transparent)\\b)/,color);
    const beforeShadows=split(before.boxShadow),afterShadows=split(after.boxShadow),remaining=[...beforeShadows];
    const changed=afterShadows.map(shadow=>{const index=remaining.indexOf(shadow);if(index>=0){remaining.splice(index,1);return false}return true});
    if(!target.dataset.browserContrastSavedStyle)target.dataset.browserContrastSavedStyle=target.getAttribute('style')??'__missing__';
    if(state==='restore'){
      const saved=target.dataset.browserContrastSavedStyle;if(saved==='__missing__')target.removeAttribute('style');else target.setAttribute('style',saved);delete target.dataset.browserContrastSavedStyle;return true;
    }
    const color=state==='black'?'rgb(0 0 0)':state==='white'?'rgb(255 255 255)':null;
    const outlineChanged=after.outlineStyle!==before.outlineStyle||after.outlineWidth!==before.outlineWidth||after.outlineColor!==before.outlineColor||after.outlineOffset!==before.outlineOffset;
    const outline=state==='hidden'?before:after;
    for(const [property,key] of [['outline-style','outlineStyle'],['outline-width','outlineWidth'],['outline-offset','outlineOffset']])target.style.setProperty(property,outline[key],'important');
    target.style.setProperty('outline-color',color&&outlineChanged?color:outline.outlineColor,'important');
    const shadows=state==='hidden'?beforeShadows:afterShadows.map((shadow,index)=>color&&changed[index]?recolor(shadow,color):shadow);
    target.style.setProperty('box-shadow',shadows.length?shadows.join(', '):'none','important');
    return true;
  })()`;
}

async function captureFocusCandidates(client, candidates) {
  const results = new Map();
  for (const candidate of candidates.filter(({ kind }) => kind === "focus")) {
    const geometry = await evaluate(
      client,
      `new Promise(resolve=>{const target=document.querySelector('[data-browser-focus-id=${JSON.stringify(candidate.raw.oracleId)}]');if(!target)throw new Error('missing focus target');target.focus();requestAnimationFrame(()=>requestAnimationFrame(()=>{const box=target.getBoundingClientRect();resolve({left:box.left,top:box.top,right:box.right,bottom:box.bottom,width:box.width,height:box.height,geometryCollectedAt:performance.now()})}))})`,
    );
    const current = {
      ...candidate,
      geometry,
      raw: {
        ...candidate.raw,
        geometryCollectedAt: geometry.geometryCollectedAt,
      },
    };
    const captures = { original: await fullPageCapture(client) };
    try {
      for (const state of ["hidden", "black", "white"]) {
        await evaluate(client, focusProbeSource(candidate, state));
        await paintFrames(client);
        captures[state] = await fullPageCapture(client);
      }
    } finally {
      await evaluate(client, focusProbeSource(candidate, "restore"));
      await paintFrames(client);
    }
    const [evidence] = await decodeAndAnalyze(client, [current], captures);
    results.set(candidate.index, evidence);
  }
  await evaluate(
    client,
    "document.body.tabIndex=-1;document.body.focus();document.body.removeAttribute('tabindex');true",
  );
  return results;
}

async function captureRenderedCandidates(client, candidates) {
  const global = await captureGlobalCandidates(client, candidates);
  const focus = await captureFocusCandidates(client, candidates);
  return candidates.map((candidate) => global.get(candidate.index) ?? focus.get(candidate.index));
}

export async function sampleRenderedContrast(client, pairs, options = {}) {
  const candidates = pairs
    .map((pair, index) => ({
      ...pair,
      index,
      geometry: pair.raw?.geometry,
    }))
    .filter(({ geometry }) => geometry);
  const captureCandidates = options.captureCandidates ?? ((values) => captureRenderedCandidates(client, values));
  const evidence = await captureCandidates(candidates);
  const byIndex = new Map(candidates.map((candidate, index) => [candidate.index, evidence[index]]));
  return pairs.map((pair, index) =>
    byIndex.has(index) ? applyRenderedSamples(pair, byIndex.get(index)) : pair,
  );
}

async function runCase({
  chrome,
  origin,
  outputDirectory,
  concept,
  viewport,
  destination,
  textScale = "standard",
  safeArea = { top: 59, right: 0, bottom: 34, left: 0 },
  query = "",
  expectedPlatform = viewport.platform,
  variant = {},
  caseIndex,
}) {
  const client = await chrome.newPage();
  const offOrigin = [];
  const requests = [];
  client.on("Network.requestWillBeSent", (event) => {
    requests.push(event.request.url);
    try {
      const parsed = new URL(event.request.url);
      if (
        !["data:", "blob:"].includes(parsed.protocol) &&
        parsed.origin !== origin
      )
        offOrigin.push(event.request.url);
    } catch {
      offOrigin.push(event.request.url);
    }
  });
  return withOwnedCleanup(async () => {
    await setupPage(client, origin, viewport, safeArea);
    await navigate(client, `${origin}/${query}`);
    const state = await evaluate(
      client,
      actionSource(
        concept,
        destination,
        textScale,
        destination === "question"
          ? { ...variant, scenario: "question" }
          : variant,
      ),
    );
    if (state.platform !== expectedPlatform)
      throw new Error(
        `platform mismatch: ${state.platform} != ${expectedPlatform}`,
      );
    const preCollectionStabilization = await stabilizePaint(client);
    const focus = await focusAudit(client);
    await evaluate(
      client,
      "document.body.tabIndex=-1;document.body.focus();document.body.removeAttribute('tabindex');true",
    );
    const postFocusStabilization = await stabilizePaint(client);
    const measurements = await measurePage(client, {
      platform: expectedPlatform,
      focusOrder: focus.focusOrder,
      focusCandidates: focus.focusCandidates,
    });
    const mergedStabilization = assembleCaseStabilization(
      preCollectionStabilization,
      focus.stabilizations,
      postFocusStabilization,
    );
    const capabilityFailures = [...mergedStabilization.capabilityFailures];
    if (viewport.visibleHeight) {
      const layoutHeight = viewport.layoutHeight ?? viewport.height;
      if (
        measurements.viewport.height !== layoutHeight ||
        measurements.viewport.visualHeight >= measurements.viewport.height ||
        Math.abs(measurements.viewport.visualHeight - viewport.visibleHeight) >
          1
      )
        capabilityFailures.push({
          code: "keyboard-emulation-unsupported",
          expected: { layoutHeight, visualHeight: viewport.visibleHeight },
          actual: measurements.viewport,
        });
    }
    const route = state.route ?? destination;
    const identity = [
      String(caseIndex).padStart(3, "0"),
      concept,
      expectedPlatform,
      `${viewport.width}x${viewport.layoutHeight ?? viewport.height}`,
      viewport.visibleHeight
        ? `visual-${viewport.visibleHeight}`
        : "visual-full",
      destination,
      textScale,
      `appearance-${variant.appearance ?? "system"}`,
      `motion-${variant.reducedMotion ? "reduce" : "system"}`,
      `scenario-${variant.scenario ?? (destination === "question" ? "question" : "baseline")}`,
      query ? "query-override" : "query-none",
      `safe-${safeArea.top}-${safeArea.right}-${safeArea.bottom}-${safeArea.left}`,
    ];
    const name = identity.join("-").replaceAll(/[^a-zA-Z0-9_.-]/g, "_");
    const file = path.join(outputDirectory, `${name}.png`);
    const screenshotData = await screenshot(client, file);
    measurements.contrastPairs = await sampleRenderedContrast(
      client,
      measurements.contrastPairs,
    );
    const violations = assertGeometry(measurements, { route, safeArea });
    const finalAudit = await evaluate(
      client,
      `({attempts:[...(window.__capabilityAttempts??[])],fileInputs:document.querySelectorAll('input[type="file"],input[capture]').length})`,
    );
    if (finalAudit.fileInputs !== 0 || finalAudit.attempts.length)
      throw new Error(`capability audit failed: ${JSON.stringify(finalAudit)}`);
    if (offOrigin.length)
      throw new Error(`off-origin request(s): ${offOrigin.join(", ")}`);
    return {
      audit: {
        capabilityAttempts: finalAudit.attempts,
        fileInputs: finalAudit.fileInputs,
        offOriginRequests: offOrigin,
        requests,
      },
      measurements,
      capabilityFailures,
      stabilization: mergedStabilization.stabilization,
      state,
      name,
      route,
      violations,
      screenshot: file,
      requestCount: requests.length,
    };
  }, [() => client.close()]);
}

async function cspCase(chrome, origin) {
  const server = net.createServer();
  let connections = 0;
  let listening = false;
  let client;
  server.on("connection", (socket) => {
    connections += 1;
    socket.destroy();
  });
  const network = [];
  const targetRequests = new Set();
  return withOwnedCleanup(async () => {
    await new Promise((resolve, reject) => {
      server.once("error", reject);
      server.listen(0, "127.0.0.1", resolve);
    });
    listening = true;
    const address = server.address();
    const target = `http://127.0.0.1:${address.port}`;
    const targetOrigin = new URL(target).origin;
    client = await chrome.newPage();
    client.on("Network.requestWillBeSent", (event) => {
      if (new URL(event.request.url).origin === targetOrigin) {
        targetRequests.add(event.requestId);
        network.push({
          type: "request",
          requestId: event.requestId,
          url: event.request.url,
        });
      }
    });
    client.on("Network.loadingFailed", (event) => {
      if (targetRequests.has(event.requestId))
        network.push({
          type: "failed",
          requestId: event.requestId,
          blockedReason: event.blockedReason,
          errorText: event.errorText,
        });
    });
    client.on("Network.responseReceived", (event) => {
      if (targetRequests.has(event.requestId))
        network.push({
          type: "response",
          requestId: event.requestId,
          status: event.response.status,
        });
    });
    await setupPage(
      client,
      origin,
      viewports.iosPortrait,
      { top: 0, right: 0, bottom: 0, left: 0 },
      false,
    );
    await navigate(client, origin);
    const result = await evaluate(
      client,
      `(async()=>{ const violations=[]; addEventListener('securitypolicyviolation',e=>violations.push(e.effectiveDirective)); const fetchResult=fetch(${JSON.stringify(`${target}/connect`)}).then(()=>false,()=>true); const imageResult=new Promise(resolve=>{const i=new Image();i.onload=()=>resolve(false);i.onerror=()=>resolve(true);i.src=${JSON.stringify(`${target}/image`)};document.body.append(i)}); const failed=await Promise.all([fetchResult,imageResult]); await new Promise(resolve=>requestAnimationFrame(()=>resolve())); return {violations,failed};})()`,
    );
    if (
      !result.violations.includes("connect-src") ||
      !result.violations.includes("img-src") ||
      result.failed.some((value) => !value) ||
      connections !== 0
    )
      throw new Error(
        `CSP isolation failed: ${JSON.stringify({ result, connections, network })}`,
      );
    if (
      network.some((event) => event.type === "response") ||
      network.some(
        (event) =>
          event.type === "request" &&
          !network.some(
            (failure) =>
              failure.type === "failed" &&
              failure.requestId === event.requestId &&
              String(failure.blockedReason).toLowerCase().includes("csp"),
          ),
      )
    )
      throw new Error(`CSP CDP audit failed: ${JSON.stringify(network)}`);
    return {
      violations: result.violations.sort(),
      failures: result.failed.length,
      connections,
      network,
    };
  }, [
    async () => {
      if (client) await client.close();
    },
    async () => {
      if (listening) await new Promise((resolve) => server.close(resolve));
    },
  ]);
}

export async function runBrowserMatrix(options = {}) {
  const outputDirectory =
    options.outputDirectory ??
    path.join(
      process.env.EVENER_SCRATCH_DIR ?? os.tmpdir(),
      `mobile-concepts-browser-${process.pid}`,
    );
  await mkdir(outputDirectory, { recursive: true });
  const caseMatch = options.caseMatch ?? process.env.BROWSER_CASE_MATCH ?? "";
  let server;
  let chrome;
  let origin;
  const results = [];
  let failed = false;
  let caseIndex = 0;
  let primaryError;
  const cleanupErrors = [];
  try {
    server = await preview({
      configFile: false,
      root: process.cwd(),
      preview: { host: "127.0.0.1", port: 0, strictPort: false },
      build: { outDir: "dist" },
    });
    const address = server.httpServer.address();
    origin = `http://127.0.0.1:${address.port}`;
    chrome = await startChrome();
    for (const concept of concepts)
      for (const viewport of [viewports.iosPortrait, viewports.androidPortrait])
        for (const destination of [
          "sessions",
          "search",
          "conversation",
          "work",
          "question",
          "new",
          "settings",
          "voice",
          "voice-speaking",
        ])
          for (const textScale of ["standard", "accessibility"]) {
            if (
              caseMatch &&
              !`${concept}-${viewport.platform}-${destination}-${textScale}`.includes(
                caseMatch,
              )
            )
              continue;
            caseIndex += 1;
            try {
              results.push(
                await runCase({
                  chrome,
                  origin,
                  outputDirectory,
                  concept,
                  viewport,
                  destination,
                  textScale,
                  caseIndex,
                }),
              );
            } catch (error) {
              failed = true;
              results.push({
                name: `${String(caseIndex).padStart(3, "0")}-${concept}-${viewport.platform}-${destination}-${textScale}`,
                error: error.message,
                violations: [],
              });
            }
          }
    for (const extra of [
      {
        tag: "safe-zero",
        concept: "stillwater",
        viewport: viewports.iosPortrait,
        destination: "sessions",
        safeArea: { top: 0, right: 0, bottom: 0, left: 0 },
      },
      {
        tag: "conversation-landscape",
        concept: "stillwater",
        viewport: viewports.landscape,
        destination: "conversation",
      },
      {
        tag: "voice-landscape",
        concept: "stillwater",
        viewport: viewports.landscape,
        destination: "voice",
      },
      {
        tag: "query-override",
        concept: "stillwater",
        viewport: viewports.iosPortrait,
        destination: "sessions",
        query: "?platform=android",
        expectedPlatform: "android",
      },
      ...[
        "loading",
        "empty",
        "offline",
        "error",
        "needs-attention",
        "multi-agent",
        "completed",
        "long-content",
      ].map((scenario) => ({
        tag: `scenario-${scenario}`,
        concept: "stillwater",
        viewport: viewports.iosPortrait,
        destination: "sessions",
        variant: { scenario },
      })),
      {
        tag: "appearance-dark",
        concept: "stillwater",
        viewport: viewports.iosPortrait,
        destination: "sessions",
        variant: { appearance: "dark" },
      },
      {
        tag: "motion-reduce",
        concept: "stillwater",
        viewport: viewports.iosPortrait,
        destination: "sessions",
        variant: { reducedMotion: true },
      },
      {
        tag: "keyboard-visual-viewport",
        concept: "stillwater",
        viewport: {
          ...viewports.iosPortrait,
          layoutHeight: viewports.iosPortrait.height,
          visibleHeight: 600,
        },
        destination: "conversation",
      },
    ]) {
      if (caseMatch && !extra.tag.includes(caseMatch)) continue;
      caseIndex += 1;
      try {
        const value = await runCase({
          chrome,
          origin,
          outputDirectory,
          caseIndex,
          ...extra,
        });
        results.push(value);
      } catch (error) {
        failed = true;
        results.push({
          name: `${String(caseIndex).padStart(3, "0")}-extra-${extra.destination}${extra.variant?.scenario ? `-${extra.variant.scenario}` : ""}`,
          error: error.message,
          violations: [],
        });
      }
    }
    let csp;
    try {
      csp = await cspCase(chrome, origin);
    } catch (error) {
      failed = true;
      csp = { error: error.message };
    }
    const violations = results.flatMap((result) => result.violations ?? []);
    const capabilityFailures = results.flatMap(
      (result) => result.capabilityFailures ?? [],
    );
    if (capabilityFailures.length) failed = true;
    const screenshotPaths = results
      .map((result) => result.screenshot)
      .filter(Boolean);
    const uniqueScreenshots = new Set(screenshotPaths);
    const pngFiles = (await readdir(outputDirectory)).filter((file) =>
      file.endsWith(".png"),
    );
    if (
      uniqueScreenshots.size !== screenshotPaths.length ||
      pngFiles.length !== screenshotPaths.length
    ) {
      failed = true;
      results.push({
        name: "screenshot-identity-audit",
        error: `screenshot identity mismatch: records=${screenshotPaths.length}, unique=${uniqueScreenshots.size}, files=${pngFiles.length}`,
        violations: [],
      });
    }
    if (!caseMatch && screenshotPaths.length !== 123) {
      failed = true;
      results.push({
        name: "screenshot-count-audit",
        error: `expected 123 unique screenshots, got ${screenshotPaths.length}`,
        violations: [],
      });
    }
    if (violations.length) failed = true;
    const evidence = path.join(outputDirectory, "results.json");
    await writeFile(
      evidence,
      `${JSON.stringify({ origin, results, csp }, null, 2)}\n`,
    );
    console.log(`Browser evidence: ${outputDirectory}`);
    console.log(
      `Cases: ${results.length}; screenshots: ${results.filter((result) => result.screenshot).length}; violations: ${violations.length}; capability failures: ${capabilityFailures.length}; case errors: ${results.filter((result) => result.error).length}`,
    );
    console.log(`CSP: ${JSON.stringify(csp)}`);
    if (failed) {
      const error = new Error(`Browser matrix RED; evidence: ${evidence}`);
      error.results = results;
      throw error;
    }
  } catch (error) {
    failed = true;
    primaryError = error;
  } finally {
    if (chrome)
      await chrome
        .close({ retainProfile: failed })
        .catch((error) => cleanupErrors.push(error));
    if (server)
      await new Promise((resolve, reject) =>
        server.httpServer.close((error) => (error ? reject(error) : resolve())),
      ).catch((error) => cleanupErrors.push(error));
  }
  if (primaryError && cleanupErrors.length)
    throw new AggregateError(
      [primaryError, ...cleanupErrors],
      "browser matrix and cleanup failed",
    );
  if (primaryError) throw primaryError;
  if (cleanupErrors.length)
    throw new AggregateError(cleanupErrors, "browser matrix cleanup failed");
  return results;
}

if (import.meta.url === `file://${process.argv[1]}`)
  runBrowserMatrix().catch((error) => {
    console.error(error);
    process.exitCode = 1;
  });
