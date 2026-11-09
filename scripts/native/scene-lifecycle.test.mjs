// Exercise the installed Expo plugin against its shipped Swift and plist inputs.
// Native compilation and installed-app activation are separate Mac gates.
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const root = fileURLToPath(new URL('../../mobile-native/', import.meta.url));
const require = createRequire(`${root}package.json`);
const { withBuildProperties } = require('expo-build-properties');
const { parse } = require('@expo/plist').default;
const app = JSON.parse(execFileSync(process.execPath, ['-e', 'process.stdout.write(JSON.stringify(require("./app.config.js").expo))'], { cwd: root, env: {}, encoding: 'utf8' }));
const props = app.plugins.find(p => Array.isArray(p) && p[0] === 'expo-build-properties')[1];
const archive = `${root}node_modules/expo/template.tgz`;
const entries = execFileSync('tar', ['-tzf', archive], { encoding: 'utf8' }).split('\n');
const templateFile = name => {
 const path = `package/ios/HelloWorld/${name}`;
 return entries.includes(path) ? execFileSync('tar', ['-xOf', archive, path], { encoding: 'utf8' }) : '';
};
const template = {
 plist: parse(templateFile('Info.plist')),
 appDelegate: templateFile('AppDelegate.swift'),
 sceneDelegate: templateFile('SceneDelegate.swift'),
};
const expoSource = name => readFileSync(`${root}node_modules/expo/ios/Expo/${name}`, 'utf8');

async function generate(input = template) {
 const config = withBuildProperties({ ...app, _internal: { projectRoot: root } }, props);
 const apply = async (name, modResults) => {
  const mod = config.mods?.ios?.[name];
  if (!mod) return modResults;
  return (await mod({ ...config, modResults, modRequest: { projectRoot: root, platform: 'ios', modName: name } })).modResults;
 };
 return {
  plist: await apply('infoPlist', structuredClone(input.plist)),
  appDelegate: (await apply('appDelegate', { language: 'swift', contents: input.appDelegate })).contents,
  sceneDelegate: (await apply('sceneDelegate', { language: 'swift', contents: input.sceneDelegate })).contents,
 };
}

const calls = (source, pattern) => (source.match(pattern) ?? []).length;

test('prebuild delegates window startup to one Expo scene and one React factory', async () => {
 assert.equal(Object.hasOwn(app.ios, 'appleTeamId'), false);
 assert.equal(app.plugins.includes('@bacons/apple-targets'), false);
 const { plist, appDelegate, sceneDelegate } = await generate();
 const manifest = plist.UIApplicationSceneManifest;
 assert.ok(manifest);
 assert.equal(manifest.UIApplicationSupportsMultipleScenes, false);
 assert.equal(manifest.UISceneConfigurations.UIWindowSceneSessionRoleApplication.length, 1);
 assert.equal(manifest.UISceneConfigurations.UIWindowSceneSessionRoleApplication[0].UISceneDelegateClassName, '$(PRODUCT_MODULE_NAME).SceneDelegate');
 // Swift symbols and call sites are machine contracts, not UI copy.
 const inheritance = appDelegate.match(/class AppDelegate\s*:\s*([^\{]+)\{/)[1].split(',').map(s => s.trim());
 assert.ok(inheritance.includes('ExpoReactNativeFactoryProvider'));
 assert.equal(calls(appDelegate, /factory\.startReactNative\s*\(/g), 0);
 assert.equal(calls(appDelegate, /reactNativeFactory\s*=\s*factory/g), 1);
 assert.equal(calls(appDelegate, /ExpoReactNativeFactory\(delegate:/g), 1);
 assert.equal(calls(appDelegate, /RCTLinkingManager\.application\s*\(/g), 0);
 assert.match(sceneDelegate, /class SceneDelegate\s*:\s*ExpoAppSceneDelegate\s*\{/);
 const sceneOwner = expoSource('ExpoAppSceneDelegate.swift');
 assert.equal(calls(sceneOwner, /factory\.startReactNative\s*\(/g), 1);
 assert.equal(calls(sceneOwner, /UIWindow\(windowScene: windowScene\)/g), 1);
 assert.match(sceneOwner, /let factory = provider\.reactNativeFactory/);
 assert.match(sceneOwner, /provider\.window = window/);
});

test('the scene forwards warm and cold links and foreground lifecycle to the shared owner', () => {
 // Check the generated subclass first so an old template fails on ownership,
 // rather than on an SDK-specific upstream source path.
 assert.match(template.sceneDelegate, /class SceneDelegate\s*:\s*ExpoAppSceneDelegate\s*\{/);
 const scene = expoSource('ExpoAppSceneDelegate.swift');
 const forwarder = expoSource('SceneEventForwarder.swift');
 assert.match(scene, /launchOptions: Self\.launchOptions\(\s*url: connectionOptions\.urlContexts\.first\?\.url,/);
 assert.match(scene, /connectionOptions\.userActivities\.forEach \{ forwarder\.continue\(\$0\) \}/);
 assert.match(scene, /forwarder\.open\(url: \$0\.url, options:/);
 assert.match(scene, /forwarder\.open\(url: context\.url, options:/);
 assert.match(scene, /forwarder\.continue\(userActivity\)/);
 assert.match(scene, /NSSelectorFromString\("scene:openURLContexts:"\)/);
 assert.match(scene, /NSSelectorFromString\("scene:continueUserActivity:"\)/);
 assert.match(forwarder, /RCTLinkingManager\.application\(application, open: url, options: options\)/);
 assert.match(forwarder, /RCTLinkingManager\.application\(application, continue: userActivity,/);
 for (const method of ['didBecomeActive', 'willResignActive', 'willEnterForeground', 'didEnterBackground']) {
  assert.equal(calls(scene, new RegExp(`forwarder\\.${method}\\(\\)`, 'g')), 1);
 }
});

test('the app supports landscape with safe-area insets', () => {
	// Expo maps "default" to portrait and landscape. The Board and conversation
	// inset their content by the left/right safe areas.
	assert.equal(app.orientation, 'default');
});

test('scene prebuild preserves application identity, linking and unrelated plist values', async () => {
 const existing = {
  CFBundleIdentifier: 'com.primeradiant.evener.native',
  CFBundleURLTypes: [{ CFBundleURLSchemes: ['evener-native'] }],
  NSAppTransportSecurity: { NSAllowsArbitraryLoads: true },
 };
 const { plist } = await generate({ ...template, plist: { ...template.plist, ...existing } });
 for (const [key, value] of Object.entries(existing)) {
  assert.deepEqual(plist[key], value);
 }
 assert.ok(plist.UIApplicationSceneManifest);
});

test('repeated scene prebuild leaves generated startup and other plugin content unchanged', async () => {
 const sentinel = '\n// external_plugin_sentinel\n';
 const once = await generate({
  plist: { ...template.plist, external_plugin_sentinel: ['kept'] },
  appDelegate: template.appDelegate + sentinel,
  sceneDelegate: template.sceneDelegate + sentinel,
 });
 assert.ok(once.appDelegate.endsWith(sentinel));
 assert.ok(once.sceneDelegate.endsWith(sentinel));
 assert.deepEqual(once.plist.external_plugin_sentinel, ['kept']);
 assert.deepEqual(await generate(once), once);
});

test('iOS generation refuses a missing team while ordinary config loading remains usable', async () => {
 const preconditions = require('./plugins/with-ios-preconditions');
 const config = preconditions({ ...app });
 await assert.rejects(() => config.mods.ios.dangerous({
  ...config,
  modResults: {},
  modRequest: { projectRoot: root, platform: 'ios', modName: 'dangerous' },
 }), Error);
 assert.equal(app.ios.bundleIdentifier, 'com.primeradiant.evener.native');
 assert.equal(app.scheme, 'evener-native');
});
