import assert from 'node:assert/strict';
import { chromium } from '@playwright/test';
import { join } from 'node:path';
import { mkdtemp, writeFile } from 'node:fs/promises';
import { failureRoot } from './diagnostics.mjs';

export async function recordWebFailure(error, phase, details) {
 // The Go driver owns TMPDIR and removes it before the caller can inspect logs.
 const directory=await mkdtemp(join(failureRoot(),'evener-web-failure-'));
 const file=join(directory,'failure.json');
 await writeFile(file,JSON.stringify({phase,message:error.message,stack:error.stack,details}),{mode:0o600});
 error.diagnosticsPath=file;
 error.tourPhase=`${phase}; private diagnostics: ${file}`;
}

// Length and containment relations to the requested path, never the text.
export function pathShape(value,requestedPaths) {
 if(typeof value!=='string')return {length:null,startsWithRequested:false,endsWithRequested:false,containsRequested:false};
 return {length:value.length,startsWithRequested:requestedPaths.some(p=>value.startsWith(p)),endsWithRequested:requestedPaths.some(p=>value.endsWith(p)),containsRequested:requestedPaths.some(p=>value.includes(p))};
}

export function pathValidationTrace(requestedPaths) {
 const events=[],pending=new Map();let sequence=0;
 const parse=payload=>{try{return JSON.parse(payload.toString());}catch{return {};}};
 return {events,
  sent(payload) {
   const frame=parse(payload);if(frame.method!=='evener/path/validate')return;
   const event={method:frame.method,event:'sent',call:++sequence,timeMS:Date.now(),requested:requestedPaths.includes(frame.params?.path),...pathShape(frame.params?.path,requestedPaths)};
   pending.set(frame.id,event);events.push(event);
  },
  received(payload) {
   const frame=parse(payload),request=pending.get(frame.id);if(!request)return;
   pending.delete(frame.id);
   events.push({method:request.method,event:'response',call:request.call,timeMS:Date.now(),elapsedMS:Date.now()-request.timeMS,valid:frame.result?.valid===true,error:Boolean(frame.error||frame.result?.error),errorCode:Number.isSafeInteger(frame.error?.code)?frame.error.code:null,requested:request.requested});
  },
 };
}

// Read-only browser probe. Never store DOM prose, paths, or raw RPC frames.
export function installDirectoryProbe(requestedPaths) {
 window.__evenerDirectoryProbe?.close();
 const events=[];let previous,identity,generation=0;
 const sample=(stage,prevented=false)=>{
  const input=document.querySelector('[role="dialog"] input[aria-label="Path"]');
  const dialog=input?.closest('[role="dialog"]');
  if(identity!==dialog){identity=dialog;generation++;}
  const button=[...(dialog?.querySelectorAll('button')??[])].find(b=>b.textContent.trim()==='Use this folder');
  const value=input?.value,shape=typeof value==='string'?{length:value.length,startsWithRequested:requestedPaths.some(p=>value.startsWith(p)),endsWithRequested:requestedPaths.some(p=>value.endsWith(p)),containsRequested:requestedPaths.some(p=>value.includes(p))}:{length:null,startsWithRequested:false,endsWithRequested:false,containsRequested:false};
  const state={generation,present:Boolean(dialog),inputMatches:requestedPaths.includes(value),inputFocused:Boolean(input)&&document.activeElement===input,selectionLength:input?input.selectionEnd-input.selectionStart:null,inputShape:shape,pending:dialog?.querySelector('[aria-label="Folders"]')?.getAttribute('aria-busy')==='true',edited:Boolean(dialog?.querySelector('[data-state="edited"]')),error:Boolean(dialog?.querySelector('[role="alert"]')),confirmEnabled:Boolean(button&&!button.disabled)};
  const encoded=JSON.stringify(state);if(stage==='mutation'&&encoded===previous)return;previous=encoded;
  events.push({stage,timeMS:Date.now(),prevented,...state});if(events.length>100)events.shift();
 };
 const listener=event=>{
  const isPath=event.target?.matches?.('input[aria-label="Path"]');
  const isSubmit=event.type==='submit'&&event.target?.querySelector?.('input[aria-label="Path"]');
  if(!isPath&&!isSubmit)return;
  if(event.type==='keydown'&&event.key!=='Enter')return;
  const stage=event.type==='keydown'?'Enter':event.type;
  sample(`before-${stage}`);queueMicrotask(()=>sample(`after-${stage}`,event.defaultPrevented));
 };
 const observer=new MutationObserver(()=>sample('mutation'));
 observer.observe(document.body,{subtree:true,childList:true,attributes:true,attributeFilter:['disabled','aria-busy','data-state']});
 for(const name of ['input','keydown','submit'])document.addEventListener(name,listener,true);
 window.__evenerDirectoryProbe={events,close(){observer.disconnect();for(const name of ['input','keydown','submit'])document.removeEventListener(name,listener,true);}};
 sample('installed');
}

export function workspaceLabels(workspace) {
 return workspace.startsWith('/private/tmp/') ? [workspace, workspace.slice(8)] : workspace.startsWith('/tmp/') ? [workspace, `/private${workspace}`] : [workspace];
}

// Read-only event evidence, not an action's success report. Labels locate
// controls, while trusted events and visibility prove their activation.
export function installProjectNavigationProbe(projectName) {
 window.__evenerProjectNavigation?.close();
 const events=[];
 const listener=event=>{
  const target=event.target.closest?.('button, [role="menuitem"], input');
  if(!target)return;
  let kind;
  if(event.type==='click'&&target.getAttribute('aria-label')===`Actions for ${projectName}`)kind='menu';
  if(event.type==='click'&&target.getAttribute('role')==='menuitem'&&target.textContent.trim()==='Project settings')kind='project';
  if(event.type==='input'&&target.type==='number')kind='edit';
  if(!kind)return;
  const rect=target.getBoundingClientRect(),style=getComputedStyle(target);
  events.push({kind,trusted:event.isTrusted,visible:rect.width>0&&rect.height>0&&style.visibility==='visible'&&style.display!=='none'});
 };
 for(const type of ['click','input'])document.addEventListener(type,listener,true);
 window.__evenerProjectNavigation={events,close(){for(const type of ['click','input'])document.removeEventListener(type,listener,true);}};
}

// Runs in the browser. Conversation panes also carry data-session-ref, so
// only a row with the rail's title can certify navigation readiness.
export function railCaptureReady(refs) {
 const rows = [...document.querySelectorAll('[data-session-ref]')].filter(row => row.querySelector('[data-testid="rail-row-title"]'));
 return refs.every(ref => {
  const matching = rows.filter(row => row.getAttribute('data-session-ref') === ref);
  return matching.length > 0 && matching.every(row => !row.querySelector('[data-testid="rail-row-not-started"]'));
 });
}

export function newSessionButton(page) {
 // Welcome can offer the same action after its lazy pane finishes loading.
 return page.getByTestId('rail').getByRole('button', { name: /^(?:\+ )?New session$/ });
}

export async function open(options) {
 const { url, authURL, workspace, viewport, outputDir } = options;
 assert.deepEqual(viewport, { width: 1280, height: 900 });
 const startedRefs = new Set();
 let phase = "browser launch";
 let browser;
 try {
 browser = await chromium.launch({ headless: true });
 } catch (error) { await recordWebFailure(error,phase); throw error; }
 try {
  const context = await browser.newContext({ viewport, deviceScaleFactor: 1, serviceWorkers: 'block', reducedMotion: 'reduce' });
  // This guard only denies external traffic. It never supplies a response.
  await context.route('**/*', route => new URL(route.request().url()).origin === new URL(url).origin ? route.continue() : route.abort());
  await context.routeWebSocket('**/*', socket => {
   const target = new URL(socket.url());
   target.protocol = target.protocol === 'wss:' ? 'https:' : 'http:';
   if (target.origin === new URL(url).origin) socket.connectToServer();
   else socket.close();
  });
  const page = await context.newPage();
  const requestedPaths=workspaceLabels(workspace),pathTrace=pathValidationTrace(requestedPaths);
  page.on('websocket',socket=>{
   socket.on('framesent',({payload})=>pathTrace.sent(payload));
   socket.on('framereceived',({payload})=>pathTrace.received(payload));
  });
  page.setDefaultTimeout(30_000);
  phase = "authentication";
  await page.goto(authURL);
  await page.waitForURL(u => !u.pathname.startsWith('/auth/'));
  async function newSession(selectedWorkspace) {
  try {
  if (new URL(page.url()).pathname.startsWith('/settings/')) await page.goto(url);
  phase = 'new session';
  await newSessionButton(page).click();
  requestedPaths.splice(0,requestedPaths.length,...workspaceLabels(selectedWorkspace));
  await page.evaluate(installDirectoryProbe,requestedPaths);
  phase = 'directory-open';
  await page.getByRole('button', { name: /^Working directory:/ }).click();
  phase = 'directory-path';
  await page.getByRole('textbox', { name: 'Path', exact: true }).fill(selectedWorkspace);
  await page.getByRole('textbox', { name: 'Path', exact: true }).press('Enter');
  phase = 'folder-confirm';
  await page.getByRole('button', { name: 'Use this folder', exact: true }).click();
  if(options.scenario==='first-provider')return;
  phase = 'model selection';
  await page.getByRole('button', { name: /change model$/i }).click();
  await page.getByRole('combobox', { name: 'Model', exact: true }).fill(options.model.split('/')[1]);
  await page.getByRole('listbox', { name: 'Model', exact: true }).getByRole('option').first().click();
  } catch (error) {
   const picker=await page.evaluate(()=>window.__evenerDirectoryProbe?.events??[]).catch(()=>[]);
   await recordWebFailure(error,phase,{rpc:pathTrace.events,picker});throw error;
  }
  }
  await newSession(workspace);
  return {
   async act(action, args = {}) {
    if (action === 'configure') {
     try {
     assert.equal(args.scope, 'project');
     assert.equal(args.field, 'max_rounds');
     assert.ok([workspace, ...(options.workspaces ?? [])].includes(args.workspace ?? workspace));
     // Each journey begins outside settings, so an earlier project's form and
     // saved status cannot satisfy the next project's readiness checks.
     if(new URL(page.url()).pathname.startsWith('/settings/')) {
      await newSessionButton(page).click();
      await page.waitForURL(u=>u.pathname==='/new');
     }
     const selectedWorkspace=args.workspace??workspace;
     const projectName=selectedWorkspace.split('/').at(-1);
     await page.evaluate(installProjectNavigationProbe,projectName);
     const menu=page.getByRole('button',{name:`Actions for ${projectName}`,exact:true});
     // The menu is visibility:hidden until its visible project row is hovered.
     await page.getByRole('treeitem').filter({has:page.getByRole('button',{name:`Actions for ${projectName}`,exact:true,includeHidden:true})}).hover();
     await menu.click();
     await page.getByRole('menuitem',{name:'Project settings',exact:true}).click();
     await page.waitForURL(u=>u.pathname==='/settings/project'&&workspaceLabels(selectedWorkspace).includes(u.searchParams.get('cwd')));
     await page.getByRole('spinbutton', { name: 'Max rounds', exact: true }).fill(String(args.value));
     await page.getByRole('button', { name: 'Save launch defaults', exact: true }).click();
     await page.locator('[data-testid="launch-config-status"][data-state="saved"]').waitFor();
     return {events:await page.evaluate(()=>window.__evenerProjectNavigation?.events??[])};
     } catch(error) {
      await recordWebFailure(error,'project navigation');throw error;
     }
    } else if(action==='setup-provider') {
     assert.equal(options.scenario,'first-provider');
     const endpoint=new URL(args.baseURL);
     assert.ok(endpoint.protocol==='http:'&&endpoint.hostname==='127.0.0.1'&&!endpoint.username&&!endpoint.password);
     assert.ok(`${args.providerID}/${args.modelID}`===options.model);
     try {
      phase='provider chooser';
      await page.getByRole('button',{name:'Connect provider',exact:true}).click();
      await page.getByTestId('show-all-providers').click();
      await page.getByRole('button',{name:'OpenAI-compatible endpoint',exact:true}).click();
      phase='provider endpoint';
      await page.getByRole('button',{name:'Configure provider',exact:true}).click();
      await page.getByRole('textbox',{name:'Name',exact:true}).fill(args.providerID);
      await page.getByRole('textbox',{name:'Base URL (optional)',exact:true}).fill(args.baseURL);
      await page.getByRole('button',{name:'Create',exact:true}).click();
      phase='provider credential';
      await page.getByLabel('API key',{exact:true}).fill(args.credential);
      await page.getByRole('button',{name:'Save and check',exact:true}).click();
      phase='provider completion';
      // A successful check closes the guided dialog and returns to the
      // preserved spawn draft on its own: there is no success continuation, and
      // no forced model picker. The model choice below is a deliberate
      // regular-UI action, not a required handoff.
      await page.getByRole('dialog').waitFor({state:'detached'});
      const cwd=page.locator('#spawn-cwd');
      assert.equal(await cwd.getAttribute('data-state'),'chosen');
      assert.ok((await cwd.getAttribute('aria-label')??'').includes(workspace));
      assert.equal(await page.getByRole('listbox',{name:'Model',exact:true}).count(),0);
      phase='provider model selection';
      await page.getByTestId('spawn-model-trigger').click();
      await page.getByRole('combobox',{name:'Model',exact:true}).fill(args.modelID);
      await page.getByRole('listbox',{name:'Model',exact:true}).getByRole('option').first().click();
     } catch(error) {
      // Do not retain locator call logs: a password fill can contain the key.
      error.tourPhase=phase;throw error;
     }
    } else if (action === 'start') {
     if (args.workspace) await newSession(args.workspace);
     const prompt = page.getByRole('textbox', { name: 'Prompt', exact: true });
     await prompt.fill('');
     const lines = args.prompt.split('\n');
     for (const [index, line] of lines.entries()) {
      if (index) await prompt.press('Shift+Enter');
      await page.keyboard.insertText(line);
     }
     await page.getByRole('button', { name: 'Start', exact: true }).click();
     try { await page.waitForURL(u => u.pathname.startsWith('/s/')); }
     catch (error) {
      error.tourPhase = 'session navigation, alerts: ' + (await page.getByRole('alert').allTextContents()).join(' | ').replace(/(?:https?:|wss?:)\/\/\S+|\/[^\s]+/g, '[private path]');
      throw error;
     }
     startedRefs.add(decodeURIComponent(new URL(page.url()).pathname.slice(3)));
    } else if (action === 'send' || action === 'steer') {
     const message = page.getByRole('textbox', { name: 'Message', exact: true });
     await message.fill('');
     await message.pressSequentially(args.prompt);
     await page.getByTestId(action === 'steer' ? 'composer-steer' : 'composer-submit').click();
    } else if (action === 'stop') {
     await page.getByTestId('composer-stop').click();
    } else if (action === 'capture') {
     assert.notEqual(options.scenario,'first-provider','provider credentials must never be captured');
     assert.ok(['entry', 'action', 'verified'].includes(args.id));
     assert.ok(!page.url().includes('/auth/'));
     if (options.scenario === 'configuration' && args.id === 'entry') await page.getByTestId('composer-stop').waitFor({ state: 'visible' });
     if (options.scenario === 'configuration' && args.id === 'action') await page.getByRole('spinbutton', { name: 'Max rounds', exact: true }).scrollIntoViewIfNeeded();
     if (options.scenario === 'configuration' && args.id === 'verified') {
      await page.locator('[data-tool-name="apply_patch"]').waitFor({ state: 'visible' });
      await page.locator('[data-testid="agent-message-item"][data-live="false"]').waitFor({ state: 'visible' });
     }
     if (options.scenario === 'daily') {
      if (args.id === 'entry') await page.getByTestId('composer-stop').waitFor({ state: 'visible' });
      if (args.id === 'action') await page.locator('[data-tool-name="shell"], [data-tool-name="exec_command"]').first().waitFor({ state: 'visible' });
     } else if (options.scenario === 'extensions') {
      if (args.id === 'entry') await page.locator('[data-testid="spawn-plugin-summary"][data-state="ready"]').waitFor({ state: 'visible' });
      if (args.id === 'action') {
       const echo = page.locator('[data-tool-name="example__echo"]');
       await echo.waitFor({ state: 'visible' });
       const disclosure = echo.getByTestId('tool-row-trigger');
       if (await disclosure.getAttribute('aria-expanded') !== 'true') await disclosure.click();
       await echo.getByTestId('tool-call-body').waitFor({ state: 'visible' });
      }
     } else if (options.scenario !== 'configuration') {
      if (args.id === 'entry') await page.locator('[data-testid="spawn-plugin-summary"][data-state="ready"]').waitFor({ state: 'visible' });
      if (args.id === 'action') await page.locator('[data-tool-name="edit_file"]').waitFor({ state: 'visible' });
     }
     if (args.id === 'verified' && options.scenario !== 'configuration') {
      const completed = page.locator('[data-testid="agent-message-item"][data-live="false"]');
      await (['daily', 'extensions'].includes(options.scenario) ? completed.nth(1) : completed).waitFor({ state: 'visible' });
     }
     if (new URL(page.url()).pathname.startsWith('/s/')) {
      await page.waitForFunction(railCaptureReady, [...startedRefs], { timeout: 10_000 });
     }
     await page.evaluate(() => document.fonts.ready);
     // Opaque screenshot masks redact only exact owned workspace labels.
     // The live DOM and all runtime evidence remain unchanged.
     const aliases = [workspace, ...(options.workspaces ?? [])].flatMap(workspaceLabels);
     const masks = aliases.map(path => page.getByText(path, { exact: true }));
     assert.ok((await Promise.all(masks.map(mask => mask.count()))).some(count => count > 0), 'owned workspace label must be present for redaction');
     await page.mouse.move(0, 0);
     await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
     const png = await page.screenshot({ path: join(outputDir, `${args.id}.png`), animations: 'disabled', mask: masks, maskColor: '#000000' });
     const dimensions = await page.evaluate(async data => {
      const image = new Image();
      image.src = `data:image/png;base64,${data}`;
      await image.decode();
      return { width: image.naturalWidth, height: image.naturalHeight };
     }, png.toString('base64'));
     assert.deepEqual(dimensions, { width: 1280, height: 900 });
    } else throw new Error(`Unsupported web action: ${action}`);
    return {};
   },
   async close() { await browser.close(); },
  };
 } catch (error) { if(!error.tourPhase)await recordWebFailure(error,phase); await browser.close(); throw error; }
}
