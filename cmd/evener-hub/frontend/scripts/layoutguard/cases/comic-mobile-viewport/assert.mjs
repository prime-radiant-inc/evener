import { evaluate, navigateTo } from "../../../browserGuardCdp.mjs";

const frame = (send) => evaluate(send, "new Promise(resolve => requestAnimationFrame(resolve))");

async function until(send, expression) {
  const deadline = Date.now() + 5000;
  do {
    const value = await evaluate(send, expression);
    if (value) return value;
    await frame(send);
  } while (Date.now() < deadline);
  throw Error(
    `Comic geometry did not settle: ${expression}: ${await evaluate(send, "window.comicGeometryError || JSON.stringify({ready:!!window.comicGeometry,font:document.querySelector('[data-comic-font]')?.dataset.comicFont,notice:!!document.querySelector('[aria-label=\"Comic font unavailable\"]')})")}`,
  );
}

function sessionGeometry() {
  const rect = (node) => {
    if (!node) return null;
    const r = node.getBoundingClientRect();
    const hit = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2);
    return { top: r.top, bottom: r.bottom, height: r.height, left: r.left, right: r.right, hit: node.contains(hit) };
  };
  const sources = [...document.querySelectorAll("[data-comic-entry]")];
  const source = window.comicGeometry.source;
  const footer = document.querySelector('[data-testid="pane-footer"]');
  return {
    height: innerHeight,
    width: innerWidth,
    font: document.querySelector('[data-comic-font]')?.dataset.comicFont,
    fallbackNotice: rect(document.querySelector('[aria-label="Comic font unavailable"]')),
    root: rect(document.querySelector("[data-comic-font][data-session-ref]")),
    host: rect(document.querySelector('[data-geometry-pane]')),
    shell: rect(document.querySelector('[data-geometry-shell]')),
    footer: rect(footer),
    footerContentBottom: footer.getBoundingClientRect().bottom - parseFloat(getComputedStyle(footer).paddingBottom),
    bodyChildren: [...document.querySelector('[data-pane-scaffold]')?.firstElementChild?.children ?? []].map(node=>({tag:node.tagName,text:node.textContent.slice(0,100),box:rect(node)})),
    composer: rect(document.querySelector("[data-comic-composer]")),
    liveness: rect(document.querySelector('[data-testid="liveness-line"]')),
    livenessText: document.querySelector('[data-testid="liveness-line"]')?.textContent,
    headingColumns: [...document.querySelector('[data-comic-composer]')?.firstElementChild?.children ?? []].map(rect),
    readerLineHeight: document.querySelector('[data-comic-text]') ? parseFloat(getComputedStyle(document.querySelector('[data-comic-text]')).lineHeight) : null,
    editor: rect(document.querySelector('[role="textbox"]')),
    submit: rect(document.querySelector('[data-testid="composer-submit"]')),
    stop: rect(document.querySelector('[data-testid="composer-stop"]')),
    controls: [...document.querySelectorAll('button[aria-label="Session actions"]'),
      [...document.querySelectorAll('button')].find(node=>node.textContent.trim()==='Appearance'),
      document.querySelector('button[aria-label="Jump to latest"]')].map(node=>({name:node?.getAttribute('aria-label')??node?.textContent,box:rect(node)})),
    menuCount: document.querySelectorAll('button[aria-label="Session actions"]').length,
    reader: rect(document.querySelector("[data-comic-scroll]")),
    draft: document.querySelector('[role="textbox"]')?.textContent,
    boxSizing: getComputedStyle(document.querySelector("[data-comic-font][data-session-ref]")).boxSizing,
    overflow: document.documentElement.scrollWidth - innerWidth,
    sourceParts: sources.length,
    literal: sources.every(
      (node) =>
        node.querySelector("[data-comic-text]")?.textContent ===
        source.slice(+node.dataset.comicStart, +node.dataset.comicEnd),
    ),
  };
}

async function readerGestures(send, width) {
  const evidence=[];
  for(const gesture of width<900?['wheel','touch']:['wheel']) {
    const before=await evaluate(send, `(() => {
      const port=document.querySelector('[data-comic-scroll]').firstElementChild;
      const r=port.getBoundingClientRect();window.comicReaderInput=[];
      for(const type of ['wheel','touchstart','touchmove','touchend'])port.addEventListener(type,event=>window.comicReaderInput.push({type,trusted:event.isTrusted}),{once:true,passive:true});
      const text=[...port.querySelectorAll('[data-comic-text]')].map(node=>node.getBoundingClientRect()).filter(rect=>rect.top<r.top).at(-1);
      return {top:port.scrollTop,max:port.scrollHeight-port.clientHeight,x:r.right-16,y:r.top+r.height/2,height:r.height,sourceDelta:text?text.top-r.top-24:null};
    })()`);
    if(before.max<=0 || before.height<=0)throw Error('native reader gesture fixture is not scrollable '+JSON.stringify(before));
    const direction=before.top>before.max/2?-1:1;
    if(gesture==='wheel') {
      if(before.sourceDelta===null)throw Error('no mounted source for trusted wheel target '+JSON.stringify(before));
      await send('Input.dispatchMouseEvent',{type:'mouseWheel',x:before.x,y:before.y,deltaX:0,deltaY:before.sourceDelta});
    } else {
      const travel=Math.min(before.height/3,100);
      await send('Input.dispatchTouchEvent',{type:'touchStart',touchPoints:[{x:before.x,y:before.y,id:1}]});
      for(const step of [0.25,0.5,0.75,1]) {
        await send('Input.dispatchTouchEvent',{type:'touchMove',touchPoints:[{x:before.x,y:before.y-direction*travel*step,id:1}]});
        await frame(send);
      }
      await send('Input.dispatchTouchEvent',{type:'touchEnd',touchPoints:[]});
    }
    let after;
    try { after=await until(send,`(() => {
      const port=document.querySelector('[data-comic-scroll]').firstElementChild;
      const saved=JSON.parse(sessionStorage.getItem('evener:comic:position:local:comic-geometry')??'null')?.position;
      return Math.abs(port.scrollTop-${before.top})>1&&saved?.mode==='away'&&window.comicReaderInput.length?{top:port.scrollTop,saved,events:window.comicReaderInput}:null;
    })()`); } catch {
      throw Error('reader gesture did not retain movement '+JSON.stringify({width,gesture,before,after:await evaluate(send,`(() => {const port=document.querySelector('[data-comic-scroll]').firstElementChild;return {top:port.scrollTop,max:port.scrollHeight-port.clientHeight,saved:sessionStorage.getItem('evener:comic:position:local:comic-geometry'),events:window.comicReaderInput};})()`)}));
    }
    if(!after.events.every(event=>event.trusted)||!after.events.some(event=>event.type===(gesture==='wheel'?'wheel':'touchmove')))throw Error('reader gesture was not trusted '+JSON.stringify(after));
    evidence.push({gesture,before,after});
  }
  return evidence;
}

async function appearancePopup(send, bottom, stage) {
  try { return await checkAppearancePopup(send,bottom); }
  catch(error) {
    const state=await evaluate(send,`(() => {
      const trigger=[...document.querySelectorAll('[data-comic-font][data-session-ref] button')].find(node=>node.textContent.trim()==='Appearance');
      const popup=document.querySelector('[role="dialog"][aria-label="Appearance"]');
      const rect=node=>{if(!node)return null;const r=node.getBoundingClientRect();return {x:r.x,y:r.y,width:r.width,height:r.height,hit:node.contains(document.elementFromPoint(r.x+r.width/2,r.y+r.height/2))};};
      return {trigger:rect(trigger),triggerExpanded:trigger?.getAttribute('aria-expanded'),popup:rect(popup),focus:{tag:document.activeElement?.tagName,label:document.activeElement?.getAttribute('aria-label'),text:document.activeElement?.textContent?.slice(0,100),trigger:document.activeElement===trigger,popup:popup?.contains(document.activeElement)},font:document.querySelector('[data-comic-font]')?.dataset.comicFont};
    })()`);
    throw Error('Appearance failure '+JSON.stringify({stage,bottom,state})+': '+error.message);
  }
}

async function checkAppearancePopup(send, bottom) {
  const snapshot = () => `(() => ({draft:document.querySelector('[role="textbox"]')?.innerHTML,reader:sessionStorage.getItem('evener:comic:position:local:comic-geometry'),height:document.querySelector('[data-comic-scroll]').getBoundingClientRect().height}))()`;
  const before = await evaluate(send,snapshot());
  const point = await until(send,`(async () => {
    const button=[...document.querySelectorAll('[data-comic-font][data-session-ref] button')].find(node=>node.textContent.trim()==='Appearance');
    if(!button||button.disabled)return null;
    const before=button.getBoundingClientRect();
    await new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));
    const r=button.getBoundingClientRect(),x=r.x+r.width/2,y=r.y+r.height/2;
    return button.isConnected&&r.width&&r.height&&Math.abs(before.x-r.x)<0.1&&Math.abs(before.y-r.y)<0.1&&button.contains(document.elementFromPoint(x,y))?{x,y}:null;
  })()`);
  await send('Input.dispatchMouseEvent',{type:'mousePressed',...point,button:'left',buttons:1,clickCount:1});
  await send('Input.dispatchMouseEvent',{type:'mouseReleased',...point,button:'left',buttons:0,clickCount:1});
  const popup = await until(send,`(() => {
    const popup=document.querySelector('[role="dialog"][aria-label="Appearance"]');if(!popup)return null;
    const r=popup.getBoundingClientRect();
    return {left:r.left,right:r.right,top:r.top,bottom:r.bottom,width:innerWidth,choices:[...popup.querySelectorAll('select')].map(node=>({name:node.labels[0].textContent.trim(),options:[...node.options].map(option=>option.value),hit:node.contains(document.elementFromPoint(node.getBoundingClientRect().x+node.clientWidth/2,node.getBoundingClientRect().y+node.clientHeight/2))}))};
  })()`);
  if(popup.left<0||popup.right>popup.width||popup.top<0||popup.bottom>bottom||popup.choices.length!==3||popup.choices.some(choice=>!choice.hit))throw Error('Appearance not confined/reachable '+JSON.stringify({popup,bottom}));
  if(JSON.stringify(popup.choices.map(choice=>choice.options))!==JSON.stringify([['comic','plain'],['room','field','white'],['neutral','happy-wave','angry','scared','point','celebrate']]))throw Error('Appearance choices changed '+JSON.stringify(popup));
  await send('Input.dispatchKeyEvent',{type:'keyDown',key:'Escape',code:'Escape',windowsVirtualKeyCode:27});
  await send('Input.dispatchKeyEvent',{type:'keyUp',key:'Escape',code:'Escape',windowsVirtualKeyCode:27});
  await until(send,`!document.querySelector('[role="dialog"][aria-label="Appearance"]')&&document.activeElement?.textContent.trim()==='Appearance'`);
  const after=await evaluate(send,snapshot());
  if(JSON.stringify(before)!==JSON.stringify(after))throw Error('Appearance toggle changed draft/source/reader '+JSON.stringify({before,after}));
  return popup;
}

function modelToolbarViewport(largeHeight, visibleHeight) {
  // Desktop headless Chrome has no retractable URL bar: vh and dvh resolve
  // equally even with mobile emulation. Record that native fact first. Then
  // resolve the REAL shell stylesheet's viewport-unit declarations against
  // explicit large/dynamic dimensions, leaving selectors and cascade intact.
  // This tests the CSS constraint, not an actual mobile toolbar implementation.
  const probe = document.createElement("div");
  document.body.append(probe);
  const native = {};
  for (const unit of ["vh", "dvh"]) {
    probe.style.height = `100${unit}`;
    native[unit] = probe.getBoundingClientRect().height;
  }
  probe.remove();
  const sheet = [...document.styleSheets].find((sheet) =>
    sheet.ownerNode?.dataset.viteDevId?.endsWith("/src/shell/AppShell.module.css"),
  );
  if (!sheet) throw Error("real shell stylesheet missing");
  const rules = [...sheet.cssRules].map((rule) => rule.cssText).join("\n");
  const substitutions = [];
  const modeled = rules.replace(/(\d+(?:\.\d+)?)(dvh|svh|lvh|vh)\b/g, (token, amount, unit) => {
    const px = (Number(amount) / 100) * (unit === "dvh" || unit === "svh" ? visibleHeight : largeHeight);
    substitutions.push({ token, px });
    return `${px}px`;
  });
  const style = document.createElement("style");
  style.id = "modeled-toolbar-viewport";
  style.textContent = modeled;
  document.head.append(style);
  return { native, largeHeight, visibleHeight, substitutions };
}

export default async function assert(_measurement, { page }) {
  const { send } = page;
  const failures = [];
  const evidence = [];
  const readSession = async () => {
    const value = await evaluate(send, `(${sessionGeometry})()`);
    if(value.readerLineHeight===null || value.reader.height < value.readerLineHeight)
      failures.push('reader cannot fit a rendered text line '+JSON.stringify(value));
    if(value.menuCount!==1 || value.controls.length!==3 || value.controls.some(({box})=>!box?.hit || !reachable(box,value.width,value.footerContentBottom)))
      failures.push('existing controls unreachable '+JSON.stringify({width:value.width,controls:value.controls,menus:value.menuCount}));
    return value;
  };
  const reachable = (box, width, height) =>
    box && box.top >= -1 && box.bottom <= height + 1 && box.left >= -1 && box.right <= width + 1;

  const fixtureURL = await evaluate(send,'location.href');
  await send('Network.enable');
  for (const blockedFonts of [false,true]) {
  await send('Network.setCacheDisabled',{cacheDisabled:true});
  await send('Network.setBlockedURLs',{urls:blockedFonts?['*/webassets/comic-fonts/*']:[]});
  await navigateTo(page,fixtureURL);
  await until(send, `window.comicGeometry && document.querySelector('[data-comic-font]')?.dataset.comicFont === '${blockedFonts?'unavailable':'ready'}'`);
  for (const width of [320, 390, 1360]) {
    await send("Emulation.setDeviceMetricsOverride", { width, height: 620, deviceScaleFactor: 1, mobile: width < 900 });
    await send("Emulation.setTouchEmulationEnabled", { enabled: width < 900 });
    await evaluate(send, 'window.scrollTo(0, 0); window.comicGeometry.render("session")');
    await until(
      send,
      '!!document.querySelector("[data-comic-composer]") && !!document.querySelector("[data-comic-entry]")',
    );
    await evaluate(send, "document.fonts.ready");
    await frame(send);
    const native = await readSession();
    evidence.push({width,blockedFonts,popup:await appearancePopup(send,620,{width,blockedFonts,phase:'native'})});
    if (blockedFonts && (!native.fallbackNotice?.hit || native.fallbackNotice.height<=0))
      failures.push('blocked fonts did not mount the actual notice '+JSON.stringify(native));
    const checkSession = (name, state) => {
      if (
        state.overflow > 1 ||
        !state.sourceParts ||
        !state.literal ||
        !state.reader ||
        state.reader.height <= 0 ||
        state.root.top < state.host.top - 1 ||
        state.root.bottom > state.host.bottom + 1 ||
        state.host.top < 24 ||
        ![state.root, state.composer, state.editor, state.submit].every((box) => reachable(box, width, 620))
      )
        failures.push(`${name} ${width}: ${JSON.stringify(state)}`);
    };
    checkSession("native session", native);
    const model = await evaluate(send, `(${modelToolbarViewport})(760, 620)`);
    await frame(send);
    const toolbar = await readSession();
    checkSession("modeled toolbar session", toolbar);
    if (model.native.dvh !== 620 || model.native.vh !== 620 || !model.substitutions.length)
      throw Error(`unexpected native viewport/unit model: ${JSON.stringify(model)}`);
    // Independently resolve the untouched declaration in the native browser.
    await evaluate(send, 'document.getElementById("modeled-toolbar-viewport").remove()');
    await evaluate(send, 'new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))');
    const restored = await readSession();
    checkSession("restored native session", restored);
    evidence.push({ width, native, model, toolbar, restored });
    try { evidence.push({width,gestures:await readerGestures(send,width)}); }
    catch(error) { failures.push(error.message); }

    // Keyboard boundary simulation, not physical Safari coverage. The layout
    // viewport stays 620px while visualViewport shrinks and scrolls independently.
    await evaluate(send, "window.comicGeometry.simulateViewport()");
    const draft = `KEYBOARD_DRAFT_${width}`;
    await evaluate(send, `(() => {
      const editor = document.querySelector('[role="textbox"]');
      editor.focus();
      const range = document.createRange();
      range.selectNodeContents(editor);
      window.getSelection().removeAllRanges();
      window.getSelection().addRange(range);
    })()`);
    await send("Input.insertText", { text: draft });
    await frame(send);
    const beforeKeyboard = await readSession();
    for (const [name, event, values, visibleBottom] of [
      ["open", "resize", { height: 440, offsetTop: 0, scale: 1 }, 440],
      ["scroll", "scroll", { offsetTop: 40 }, 480],
      ["pinch", "resize", { scale: 2 }, 620],
      ["unpinch", "resize", { scale: 1 }, 480],
      ["close", "resize", { height: 620, offsetTop: 0 }, 620],
    ]) {
      await evaluate(send, `window.comicGeometry.viewport(${JSON.stringify(event)}, ${JSON.stringify(values)})`);
      await frame(send);
      const state = await readSession();
      const bottom = width < 900 ? visibleBottom : 620;
      if (
        state.boxSizing !== "border-box" ||
        state.shell.bottom !== 620 ||
        Math.abs(state.root.bottom - bottom) > 1 ||
        state.overflow > 1 ||
        state.reader.height <= 0 ||
        !state.sourceParts ||
        !state.literal ||
        state.draft !== draft ||
        !state.editor.hit ||
        !state.submit.hit ||
        ![state.composer, state.editor, state.submit].every((box) => reachable(box, width, bottom)) ||
        Math.abs(state.footer.bottom - bottom) > 1
      ) failures.push(`keyboard ${name} ${width}: expected bottom ${bottom}, ${JSON.stringify(state)}`);
      if ((name === "close" || width >= 900) && Math.abs(state.reader.height - beforeKeyboard.reader.height) > 1)
        failures.push(`keyboard ${name} ${width}: reader height did not restore`);
      evidence.push({ width, keyboard: name, composerBottom: state.composer.bottom, readerHeight: state.reader.height });
    }
    if (width < 900) {
      // The compact pane must retain a positive reader even at this height.
      for (const [name, event, values, bottom] of [
        ["short-open", "resize", { height: 360, offsetTop: 0 }, 360],
        ["short-scroll", "scroll", { offsetTop: 40 }, 400],
        ["short-close", "resize", { height: 620, offsetTop: 0 }, 620],
      ]) {
        await evaluate(send, `window.comicGeometry.viewport(${JSON.stringify(event)}, ${JSON.stringify(values)})`);
        await frame(send);
        const state = await readSession();
        if (
          state.overflow > 1 ||
          state.reader.height <= 0 ||
          !state.sourceParts ||
          state.draft !== draft ||
          !state.literal ||
          !state.editor.hit ||
          !state.submit.hit ||
          ![state.composer, state.editor, state.submit].every((box) => reachable(box, width, bottom)) ||
          Math.abs(state.footer.bottom - bottom) > 1
        ) failures.push(`keyboard ${name} ${width}: expected bottom ${bottom}, ${JSON.stringify(state)}`);
        if (name === "short-close" && (
          !state.sourceParts ||
          Math.abs(state.reader.height - beforeKeyboard.reader.height) > 1
        )) failures.push(`keyboard ${name} ${width}: source reader did not restore ${JSON.stringify(state)}`);
        evidence.push({ width, keyboard: name, composerBottom: state.composer.bottom, readerHeight: state.reader.height });
      }
    }
    if (width < 900) {
      // CDP changes the browser's env() value, not the production padding rule.
      // An independent probe proves that a nonzero safe area actually took effect.
      await send("Emulation.setSafeAreaInsetsOverride", { insets: { bottom: 34 } });
      const safeArea = await evaluate(send, `(() => {
        const probe = document.createElement("div");
        probe.style.paddingBottom = "env(safe-area-inset-bottom)";
        document.body.append(probe);
        const value = parseFloat(getComputedStyle(probe).paddingBottom);
        probe.remove();
        return value;
      })()`);
      if (safeArea !== 34) throw Error(`safe-area environment did not apply: ${safeArea}`);
      await frame(send);
      for (const control of ["submit", "stop"]) {
        if (control === "stop") {
          // Native editing clears the draft so the real active-thread composer
          // switches to Stop. No RPC or action implementation is replaced.
          await evaluate(send, `(() => {
            const editor = document.querySelector('[role="textbox"]');
            editor.focus();
            const range = document.createRange();
            range.selectNodeContents(editor);
            window.getSelection().removeAllRanges();
            window.getSelection().addRange(range);
          })()`);
          await send("Input.dispatchKeyEvent", { type: "keyDown", key: "Backspace", code: "Backspace", windowsVirtualKeyCode: 8 });
          await send("Input.dispatchKeyEvent", { type: "keyUp", key: "Backspace", code: "Backspace", windowsVirtualKeyCode: 8 });
          await evaluate(send, "window.comicGeometry.running(true)");
          await until(send, '!!document.querySelector(\'[data-testid="composer-stop"]:not(:disabled)\')');
        }
        const baseline = await readSession();
        for (const [name, event, values, bottom] of [
          ["closed", "resize", { height: 620, offsetTop: 0, scale: 1 }, 586],
          ["open", "resize", { height: 440 }, 440],
          ["offset", "scroll", { offsetTop: 40 }, 480],
          ["pinch", "resize", { scale: 2 }, 586],
          ["unpinch", "resize", { scale: 1 }, 480],
          ["close", "resize", { height: 620, offsetTop: 0 }, 586],
          ["short-open", "resize", { height: 360 }, 360],
          ["short-offset", "scroll", { offsetTop: 40 }, 400],
          ["short-close", "resize", { height: 620, offsetTop: 0 }, 586],
          ["partial-close", "resize", { height: 608 }, 586],
          ["recovered", "resize", { height: 620 }, 586],
        ]) {
          await evaluate(send, `window.comicGeometry.viewport(${JSON.stringify(event)}, ${JSON.stringify(values)})`);
          await frame(send);
          const state = await readSession();
          if(name==='short-open')evidence.push({width,blockedFonts,control,popup:await appearancePopup(send,bottom,{width,blockedFonts,control,phase:name})});
          if (
            state.boxSizing !== "border-box" ||
            state.shell.bottom !== 620 ||
            state.overflow > 1 ||
            state.draft !== (control === "submit" ? draft : "") ||
            !state.literal ||
            !state.editor.hit ||
            !state[control]?.hit ||
            ![state.composer, state.editor, state[control]].every((box) => reachable(box, width, bottom)) ||
            Math.abs(state.footerContentBottom - bottom) > 1
          ) failures.push(`safe-area ${control} ${name} ${width}: expected bottom ${bottom}, ${JSON.stringify(state)}`);
          if (state.reader.height <= 0 || !state.sourceParts)
            failures.push(`safe-area ${control} ${name} ${width}: reader collapsed ${JSON.stringify(state)}`);
          if (bottom === 586 && Math.abs(state.reader.height - baseline.reader.height) > 1)
            failures.push(`safe-area ${control} ${name} ${width}: reader did not restore ${JSON.stringify(state)}`);
          evidence.push({ width, blockedFonts, keyboard: `safe-area-${control}-${name}`, composerBottom: state.composer.bottom, readerHeight: state.reader.height, readerLineHeight:state.readerLineHeight });
        }
      }
      await evaluate(send, "window.comicGeometry.running(false)");
      await send("Emulation.setSafeAreaInsetsOverride", { insets: {} });
    }
    await evaluate(send, "window.comicGeometry.restoreViewport()");
  }
  }
  await send('Network.setBlockedURLs',{urls:[]});
  await send('Network.setCacheDisabled',{cacheDisabled:false});
  return {
    pass: !failures.length,
    reason: failures.length
      ? failures.join("\n")
      : `320/390/1360px pane reader, native wheel/touch source movement, real session/composer within 620px under native and modeled 760px large viewport, simulated visualViewport keyboard open/scroll/pinch/close, draft/source retained and reader restored, browser env safe-area 34px with Send/Stop and 440/360px keyboards without double counting. ${JSON.stringify(evidence)}`,
  };
}
