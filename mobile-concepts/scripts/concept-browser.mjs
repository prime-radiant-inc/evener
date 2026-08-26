import { mkdir, writeFile } from "node:fs/promises";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { preview } from "vite";
import { evaluate, navigate } from "./browser/cdp.mjs";
import { startChrome } from "./browser/chrome.mjs";
import { assertGeometry, measurePage } from "./browser/geometry.mjs";

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

function actionSource(concept, destination, textScale, variant = {}) {
  return `(async () => {
    const frame = () => new Promise(resolve => requestAnimationFrame(() => resolve()));
    const controls = () => [...document.querySelectorAll('button,input,textarea,select,a[href]')];
    const waitFor = selector => new Promise(resolve => { const current=document.querySelector(selector); if(current){resolve(current);return} const observer=new MutationObserver(()=>{const found=document.querySelector(selector);if(found){observer.disconnect();resolve(found)}});observer.observe(document,{subtree:true,childList:true,attributes:true}); });
    const named = name => controls().find(el => (el.getAttribute('aria-label') || el.textContent || '').trim() === name);
    const click = async name => { const el = named(name); if (!el) throw new Error('missing real control: ' + name); el.click(); await frame(); await frame(); return el; };
    const selectorClick = async selector => { const holder = document.querySelector(selector); const el = holder?.matches('button,a,input') ? holder : holder?.querySelector('button,a,input'); if (!el) throw new Error('missing real control: ' + selector); el.click(); await frame(); await frame(); };
    const input = async (label, value) => { const el = controls().find(node => node.getAttribute('aria-label') === label); if (!el) throw new Error('missing real field: ' + label); const setter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), 'value')?.set; setter.call(el, value); el.dispatchEvent(new Event('input', {bubbles:true})); el.dispatchEvent(new Event('change', {bubbles:true})); await frame(); };
    await frame(); await frame();
    if (!document.querySelector('[data-testid="concept-gallery"]')) throw new Error('case storage was not isolated');
    await click(${JSON.stringify(`Select ${conceptNames[concept]}`)});
    await waitFor('main[data-route="sessions"]');
    if (${JSON.stringify(variant.scenario ?? null)}) { await click('Lab Controls'); const scenario = [...document.querySelectorAll('input[name="scenario"]')].find(el => el.parentElement?.textContent?.trim() === ${JSON.stringify(variant.scenario ?? "")}); if (!scenario) throw new Error('missing scenario control'); scenario.click(); await frame(); if (named('Close Lab Controls')) await click('Close Lab Controls'); }
    if (${JSON.stringify(variant.appearance ?? null)}) { await click('Settings'); const appearance = controls().find(el => el.getAttribute('aria-label') === 'Appearance'); if (!appearance) throw new Error('missing Appearance'); const setter=Object.getOwnPropertyDescriptor(Object.getPrototypeOf(appearance),'value').set; setter.call(appearance,${JSON.stringify(variant.appearance ?? "")}); appearance.dispatchEvent(new Event('change',{bubbles:true})); await frame(); }
    if (${JSON.stringify(Boolean(variant.reducedMotion))}) { if (!document.querySelector('[role="dialog"]')) await click('Lab Controls'); const reduced = controls().find(el => el.getAttribute('aria-label') === 'Reduce motion' || el.parentElement?.textContent?.includes('Reduce motion')); if (!reduced) throw new Error('missing reduced motion control'); reduced.click(); await frame(); }
    if (${JSON.stringify(destination)} === 'search') { await click('Search'); await input('Search', 'Fixture-first plan'); }
    if (['conversation','work','voice','voice-speaking'].includes(${JSON.stringify(destination)})) { await selectorClick('[data-session-id="session-native-client"]'); }
    if (${JSON.stringify(destination)} === 'question') await selectorClick('[data-session-id="session-mobile-release"]');
    if (${JSON.stringify(destination)} === 'conversation') await click('Inspect fixture schema');
    if (${JSON.stringify(destination)} === 'work') { await click('Work'); await selectorClick('[data-work-node-id]'); }
    if (${JSON.stringify(destination)} === 'question') { const option = await waitFor('[data-question-id] input'); option.click(); await frame(); await input('Note', 'Browser matrix note'); }
    if (${JSON.stringify(destination)} === 'new') { await click('New Session'); await selectorClick('[data-project-id]'); await input('Prompt', 'Browser matrix populated prompt'); }
    if (${JSON.stringify(destination)} === 'settings') { await click('Settings'); await click('Lab Controls'); }
    if (${JSON.stringify(destination)}.startsWith('voice')) { await click('Voice'); await click('Set voice state: listening'); ${destination === "voice-speaking" ? "await click('Set voice state: speaking');" : ""} }
    if (${JSON.stringify(textScale)} === 'accessibility') { if (!document.querySelector('[role="dialog"]')) await click('Lab Controls'); const radio = document.querySelector('input[name="text-scale"][value="accessibility"]') || [...document.querySelectorAll('input[type="radio"]')].find(el => el.parentElement?.textContent?.trim() === 'accessibility'); if (!radio) throw new Error('missing accessibility text control'); radio.click(); await frame(); if (${JSON.stringify(destination)} !== 'settings') await click('Close Lab Controls'); }
    return { route: document.querySelector('main')?.dataset.route, platform: document.documentElement.dataset.platform, fileInputs: document.querySelectorAll('input[type="file"],input[capture]').length, attempts: window.__capabilityAttempts };
  })()`;
}

async function focusAudit(client) {
  const expected = await evaluate(
    client,
    `(() => [...document.querySelectorAll('button:not([disabled]),input:not([disabled]):not([type="hidden"]),textarea:not([disabled]),select:not([disabled]),a[href],[tabindex]:not([tabindex="-1"])')].filter(e => {const r=e.getBoundingClientRect(),s=getComputedStyle(e);const radioSkipped=e.matches('input[type="radio"]')&&!e.checked&&document.querySelector('input[type="radio"][name="'+CSS.escape(e.name)+'"]:checked');return r.width>0&&r.height>0&&s.visibility!=='hidden'&&!e.closest('[inert],[aria-hidden="true"]')&&!radioSkipped}).map((e,i)=>{e.dataset.browserFocusId='focus-'+i;return e.dataset.browserFocusId}))()`,
  );
  const inspectFocus = () =>
    evaluate(
      client,
      `(() => { const e=document.activeElement,s=getComputedStyle(e),label=e.labels?.[0],ls=label?getComputedStyle(label):null; const indicator=style=>style&&(style.outlineStyle!=='none'&&parseFloat(style.outlineWidth)>0 || (style.boxShadow!=='none'&&!style.boxShadow.includes('rgba(0, 0, 0, 0)'))); const id=e.dataset.browserFocusId || ''; return {id, subject:e.id||e.getAttribute('aria-label')||e.textContent?.trim().slice(0,80)||e.tagName, visible:indicator(s)||indicator(ls)}; })()`,
    );
  const actual = [];
  let initial = await inspectFocus();
  if (initial.id) {
    for (const modifiers of [8, 0]) {
      await client.send("Input.dispatchKeyEvent", {
        type: "keyDown",
        key: "Tab",
        code: "Tab",
        modifiers,
        windowsVirtualKeyCode: 9,
      });
      await client.send("Input.dispatchKeyEvent", {
        type: "keyUp",
        key: "Tab",
        code: "Tab",
        modifiers,
        windowsVirtualKeyCode: 9,
      });
    }
    initial = await inspectFocus();
    actual.push(initial);
  } else
    await evaluate(
      client,
      "document.body.tabIndex=-1;document.body.focus();document.body.removeAttribute('tabindex');true",
    );
  for (let index = actual.length; index < expected.length; index += 1) {
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
    actual.push(await inspectFocus());
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
  return actual.map((item, index) => ({
    id: item.subject,
    order: expected.indexOf(item.id) + 1,
    visible: item.visible,
    actualOrder: index + 1,
  }));
}

async function setupPage(client, origin, viewport, safeArea, sentinels = true) {
  const ua = agents[viewport.platform];
  await Promise.all([
    client.send("Page.enable"),
    client.send("Runtime.enable"),
    client.send("Network.enable"),
  ]);
  await client.send("Storage.clearDataForOrigin", {
    origin,
    storageTypes: "all",
  });
  await client.send("Emulation.setDeviceMetricsOverride", {
    width: viewport.width,
    height: viewport.height,
    deviceScaleFactor: 2,
    mobile: true,
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
}) {
  const client = await chrome.newPage();
  const offOrigin = [];
  const requests = [];
  client.on("Network.requestWillBeSent", (event) => {
    requests.push(event.request.url);
    if (
      !event.request.url.startsWith(origin) &&
      !/^(data|blob):/.test(event.request.url)
    )
      offOrigin.push(event.request.url);
  });
  try {
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
    if (state.fileInputs !== 0 || state.attempts.length)
      throw new Error(`capability audit failed: ${JSON.stringify(state)}`);
    if (offOrigin.length)
      throw new Error(`off-origin request(s): ${offOrigin.join(", ")}`);
    const focusOrder = await focusAudit(client);
    const measurements = await measurePage(client, {
      platform: expectedPlatform,
      viewport,
      safeArea,
      focusOrder,
      keyboardTop: viewport.height,
    });
    const route = state.route ?? destination;
    const violations = assertGeometry(measurements, { route });
    const name = `${concept}-${expectedPlatform}-${destination}-${textScale}-${safeArea.top}-${safeArea.bottom}${variant.scenario ? `-${variant.scenario}` : ""}`;
    const file = path.join(outputDirectory, `${name}.png`);
    await screenshot(client, file);
    return {
      audit: {
        capabilityAttempts: state.attempts,
        fileInputs: state.fileInputs,
        offOriginRequests: offOrigin,
        requests,
      },
      measurements,
      name,
      route,
      violations,
      screenshot: file,
      requestCount: requests.length,
    };
  } finally {
    await client.close();
  }
}

async function cspCase(chrome, origin) {
  const server = net.createServer();
  let connections = 0;
  server.on("connection", (socket) => {
    connections += 1;
    socket.destroy();
  });
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  const address = server.address();
  const target = `http://127.0.0.1:${address.port}`;
  const client = await chrome.newPage();
  const network = [];
  const targetRequests = new Set();
  client.on("Network.requestWillBeSent", (event) => {
    if (event.request.url.startsWith(target)) {
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
  try {
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
  } finally {
    await client.close();
    await new Promise((resolve) => server.close(resolve));
  }
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
  const server = await preview({
    configFile: false,
    root: process.cwd(),
    preview: { host: "127.0.0.1", port: 0, strictPort: false },
    build: { outDir: "dist" },
  });
  const address = server.httpServer.address();
  const origin = `http://127.0.0.1:${address.port}`;
  const chrome = await startChrome();
  const results = [];
  let failed = false;
  try {
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
                }),
              );
            } catch (error) {
              failed = true;
              results.push({
                name: `${concept}-${viewport.platform}-${destination}-${textScale}`,
                error: error.message,
                violations: [],
              });
            }
          }
    for (const extra of [
      {
        concept: "stillwater",
        viewport: viewports.iosPortrait,
        destination: "sessions",
        safeArea: { top: 0, right: 0, bottom: 0, left: 0 },
      },
      {
        concept: "stillwater",
        viewport: viewports.landscape,
        destination: "conversation",
      },
      {
        concept: "stillwater",
        viewport: viewports.landscape,
        destination: "voice",
      },
      {
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
        concept: "stillwater",
        viewport: viewports.iosPortrait,
        destination: "sessions",
        variant: { scenario },
      })),
      {
        concept: "stillwater",
        viewport: viewports.iosPortrait,
        destination: "sessions",
        variant: { appearance: "dark" },
      },
      {
        concept: "stillwater",
        viewport: viewports.iosPortrait,
        destination: "sessions",
        variant: { reducedMotion: true },
      },
      {
        concept: "stillwater",
        viewport: { ...viewports.iosPortrait, height: 600 },
        destination: "conversation",
      },
    ]) {
      if (caseMatch) continue;
      try {
        const value = await runCase({
          chrome,
          origin,
          outputDirectory,
          ...extra,
        });
        results.push(value);
      } catch (error) {
        failed = true;
        results.push({
          name: `extra-${extra.destination}${extra.variant?.scenario ? `-${extra.variant.scenario}` : ""}`,
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
    if (violations.length) failed = true;
    const evidence = path.join(outputDirectory, "results.json");
    await writeFile(
      evidence,
      `${JSON.stringify({ origin, results, csp }, null, 2)}\n`,
    );
    console.log(`Browser evidence: ${outputDirectory}`);
    console.log(
      `Cases: ${results.length}; screenshots: ${results.filter((result) => result.screenshot).length}; violations: ${violations.length}; case errors: ${results.filter((result) => result.error).length}`,
    );
    console.log(`CSP: ${JSON.stringify(csp)}`);
    if (failed) {
      const error = new Error(`Browser matrix RED; evidence: ${evidence}`);
      error.results = results;
      throw error;
    }
    return results;
  } finally {
    await chrome.close({ retainProfile: failed });
    await new Promise((resolve) => server.httpServer.close(resolve));
  }
}

if (import.meta.url === `file://${process.argv[1]}`)
  runBrowserMatrix().catch((error) => {
    console.error(error);
    process.exitCode = 1;
  });
