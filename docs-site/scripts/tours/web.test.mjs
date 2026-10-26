import assert from 'node:assert/strict';
import { test } from 'node:test';
import { chromium } from '@playwright/test';
import * as web from './web.mjs';
import { readFile, rm, stat, mkdtemp, mkdir } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';

test('real browser launch failure retains private diagnostics without leaking driver inputs',async t=>{
 const root=await mkdtemp(join(tmpdir(),'tour-launch-failure-'));
 t.after(()=>rm(root,{recursive:true,force:true}));
 const cache=join(root,'private-browser-canary');await mkdir(cache);
 const result=spawnSync(process.execPath,[fileURLToPath(new URL('./driver.mjs',import.meta.url))],{
  env:{...process.env,PLAYWRIGHT_BROWSERS_PATH:cache},encoding:'utf8',
  input:JSON.stringify({id:1,action:'open',args:{url:'http://127.0.0.1:1',authURL:'http://127.0.0.1:1/auth/private-auth-canary',workspace:join(root,'private-workspace-canary'),outputDir:root,viewport:{width:1280,height:900}}})+'\n',
 });
 assert.equal(result.error,undefined);
 assert.equal(result.status,1);
 const reply=JSON.parse(result.stdout);
 assert.equal(reply.id,1);assert.equal(reply.ok,false);
 // Extract the diagnostic locator, not Playwright's human-readable error.
 const file=reply.data.error.match(/private diagnostics: ([^)]+)/)?.[1];
 assert.ok(file);
 t.after(()=>rm(dirname(file),{recursive:true,force:true}));
 const diagnostic=JSON.parse(await readFile(file,'utf8'));
 assert.equal(diagnostic.phase,'browser launch');
 assert.ok(diagnostic.message.includes(cache));
 assert.ok(diagnostic.stack.includes(cache));
 for(const secret of [cache,'private-browser-canary','private-auth-canary','private-workspace-canary']) {
  assert.equal(result.stdout.includes(secret),false);
  assert.equal(result.stderr.includes(secret),false);
 }
 if(process.platform!=='win32') {
  assert.equal((await stat(dirname(file))).mode&0o777,0o700);
  assert.equal((await stat(file)).mode&0o777,0o600);
 }
});

test('web failure diagnostics retain details privately and expose only phase and path', async t => {
 const error=new Error('private-locator-canary\nCall log: locator click failed');
 assert.equal(typeof web.recordWebFailure,'function');
 await web.recordWebFailure(error,'folder-confirm');
 const file=error.diagnosticsPath;t.after(()=>rm(dirname(file),{recursive:true,force:true}));
 assert.equal(error.tourPhase,`folder-confirm; private diagnostics: ${file}`);
 assert.ok(!error.tourPhase.includes('private-locator-canary'));
 assert.deepEqual(JSON.parse(await readFile(file,'utf8')),{phase:'folder-confirm',message:error.message,stack:error.stack});
 if(process.platform!=='win32') {
  assert.equal((await stat(dirname(file))).mode&0o777,0o700);
  assert.equal((await stat(file)).mode&0o777,0o600);
 }
});

test('workspace labels include only exact temporary path aliases', () => {
 assert.deepEqual(web.workspaceLabels('/private/tmp/owned/work'), ['/private/tmp/owned/work', '/tmp/owned/work']);
 assert.deepEqual(web.workspaceLabels('/tmp/owned/work'), ['/tmp/owned/work', '/private/tmp/owned/work']);
 for (const path of ['/tmp', '/private/tmp', '/tmp-other/work', '/private/tmp-other/work', '/work/tmp/owned', 'tmp/owned', '/Users/example/work']) {
  assert.deepEqual(web.workspaceLabels(path), [path]);
 }
});

test('project navigation probe records browser activations, not synthetic clicks or route changes',async()=>{
 const browser=await chromium.launch({headless:true});
 try {
  const page=await browser.newPage();
  await page.setContent('<button aria-label="Actions for fixture">Menu</button><button role="menuitem">Project settings</button><input type="number" aria-label="Max rounds">');
  await page.evaluate(web.installProjectNavigationProbe,'fixture');
  await page.evaluate(()=>document.querySelector('button').click());
  assert.deepEqual(await page.evaluate(()=>window.__evenerProjectNavigation.events),[{kind:'menu',trusted:false,visible:true}]);
  await page.evaluate(web.installProjectNavigationProbe,'fixture');
  await page.getByRole('button',{name:'Actions for fixture'}).click();
  await page.getByRole('menuitem',{name:'Project settings'}).click();
  await page.getByRole('spinbutton').fill('7');
  assert.deepEqual(await page.evaluate(()=>window.__evenerProjectNavigation.events),['menu','project','edit'].map(kind=>({kind,trusted:true,visible:true})));
  await page.goto('about:blank');
  assert.deepEqual(await page.evaluate(()=>window.__evenerProjectNavigation?.events??[]),[]);
 } finally {await browser.close();}
});

test('path-validation diagnostics retain only allowlisted fields and path equality', () => {
 assert.equal(typeof web.pathValidationTrace,'function');
 const trace=web.pathValidationTrace(['/tmp/private-path-canary']);
 trace.sent(JSON.stringify({id:1,method:'auth',params:{token:'credential-canary'}}));
 trace.received(JSON.stringify({id:1,result:{token:'credential-canary'}}));
 assert.deepEqual(trace.events,[]);
 trace.sent(JSON.stringify({id:2,method:'evener/path/validate',params:{path:'/tmp/private-path-canary',kind:'dir',token:'credential-canary'}}));
 trace.received(JSON.stringify({id:2,result:{valid:true,path:'/tmp/private-path-canary',secret:'credential-canary'}}));
 trace.sent(JSON.stringify({id:3,method:'evener/path/validate',params:{path:'/unrelated-private-path'}}));
 trace.received(JSON.stringify({id:3,error:{code:-32603,message:'credential-canary'}}));
 assert.equal(trace.events.length,4);
 assert.equal(trace.events[0].requested,true);
 assert.equal(trace.events[1].valid,true);
 assert.equal(trace.events[2].requested,false);
 assert.equal(trace.events[3].errorCode,-32603);
 assert.ok(trace.events.every(e=>e.method==='evener/path/validate' && Number.isFinite(e.timeMS)));
 assert.ok(!JSON.stringify(trace.events).includes('canary'));
 assert.ok(!JSON.stringify(trace.events).includes('private-path'));
});

test('capture readiness requires every started session in the rail without a never-run marker', async () => {
 assert.equal(typeof web.railCaptureReady, 'function', 'capture must expose its real DOM readiness predicate');
 const browser = await chromium.launch({ headless: true });
 try {
  const page = await browser.newPage();
  const ready = refs => page.evaluate(web.railCaptureReady, refs);
  await page.setContent('<main data-session-ref="local:one"></main>');
  assert.equal(await ready(['local:one']), false, 'a conversation pane is not a rail row');
  await page.setContent('<span data-session-ref="local:one"><button data-testid="rail-row-title"></button><span data-testid="rail-row-not-started"></span></span>');
  assert.equal(await ready(['local:one']), false, 'indexed never-run state cannot certify submitted work');
  await page.locator('[data-testid="rail-row-not-started"]').evaluate(node => node.remove());
  assert.equal(await ready(['local:one']), true);
  assert.equal(await ready(['local:one', 'local:two']), false, 'an earlier missing session must still block capture');
  await page.setContent('<span data-session-ref="local:one"><button data-testid="rail-row-title"></button></span><span data-session-ref="local:two"><button data-testid="rail-row-title"></button></span>');
  assert.equal(await ready(['local:one', 'local:two']), true);
  assert.equal(await ready([]), true, 'pre-launch checkpoints need no session rows');
 } finally {
  await browser.close();
 }
});

test('path shape relates a value to the requested path without storing it',()=>{
 assert.deepEqual(web.pathShape('/home/x/tmp/owned',['/tmp/owned']),{length:17,startsWithRequested:false,endsWithRequested:true,containsRequested:true});
 assert.deepEqual(web.pathShape('/tmp/owned',['/tmp/owned']),{length:10,startsWithRequested:true,endsWithRequested:true,containsRequested:true});
 assert.deepEqual(web.pathShape(undefined,['/tmp/owned']),{length:null,startsWithRequested:false,endsWithRequested:false,containsRequested:false});
});

test('picker probe observes edited and confirmation state without storing input text',async()=>{
 const browser=await chromium.launch({headless:true});
 try {
  const page=await browser.newPage();
  await page.setContent('<div role="dialog"><form><input aria-label="Path"><section aria-label="Folders" aria-busy="true"></section><button disabled>Use this folder</button></form></div>');
  await page.evaluate(web.installDirectoryProbe,['/tmp/private-path-canary']);
  await page.getByRole('textbox',{name:'Path',exact:true}).fill('/tmp/private-path-canary');
  await page.evaluate(()=>{
   document.querySelector('input').dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true}));
   document.querySelector('section').setAttribute('aria-busy','false');
   document.querySelector('button').disabled=false;
  });
  const events=await page.evaluate(()=>window.__evenerDirectoryProbe.events);
  assert.ok(events.some(e=>e.inputMatches));
  assert.ok(events.some(e=>e.inputShape.endsWithRequested&&e.inputShape.length==='/tmp/private-path-canary'.length));
  assert.ok(events.some(e=>e.stage==='before-Enter'));
  assert.ok(events.some(e=>e.stage==='after-Enter'));
  assert.ok(events.some(e=>e.confirmEnabled&&!e.pending));
  assert.ok(!JSON.stringify(events).includes('canary'));
 } finally {await browser.close();}
});
