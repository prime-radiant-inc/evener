// Exercise the installed Expo plugin against its shipped Swift template. This
// is generated lifecycle coverage, not a check that the config repeats a flag.
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const root = fileURLToPath(new URL('../../mobile-native/', import.meta.url));
const require = createRequire(`${root}package.json`);
const { withBuildProperties } = require('expo-build-properties');
const app = JSON.parse(execFileSync(process.execPath, ['-e', 'process.stdout.write(JSON.stringify(require("./app.config.js").expo))'], { cwd: root, env: {}, encoding: 'utf8' }));
const props = app.plugins.find(p => Array.isArray(p) && p[0] === 'expo-build-properties')[1];
const template = execFileSync('tar', ['-xOf', `${root}node_modules/expo/template.tgz`, 'package/ios/HelloWorld/AppDelegate.swift'], { encoding: 'utf8' });

async function generate(input = { plist: {}, swift: template }) {
 const config = withBuildProperties({ ...app, _internal: { projectRoot: root } }, props);
 const apply = async (name, modResults) => {
  const mod = config.mods?.ios?.[name];
  if (!mod) return modResults;
  return (await mod({ ...config, modResults, modRequest: { projectRoot: root, platform: 'ios', modName: name } })).modResults;
 };
 return { plist: await apply('infoPlist', structuredClone(input.plist)), swift: (await apply('appDelegate', { language: 'swift', contents: input.swift })).contents };
}

test('prebuild delegates window startup to the Expo scene lifecycle', async () => {
 assert.equal(Object.hasOwn(app.ios, 'appleTeamId'), false);
 assert.equal(app.plugins.includes('@bacons/apple-targets'), false);
 const { plist, swift } = await generate();
 const manifest = plist.UIApplicationSceneManifest;
 assert.ok(manifest);
 assert.equal(manifest.UIApplicationSupportsMultipleScenes, false);
 assert.equal(manifest.UISceneConfigurations.UIWindowSceneSessionRoleApplication.length, 1);
 assert.equal(manifest.UISceneConfigurations.UIWindowSceneSessionRoleApplication[0].UISceneDelegateClassName, 'EXExpoAppSceneDelegate');
 // Swift symbols and call sites are machine contracts, not UI copy.
 const inheritance = swift.match(/class AppDelegate\s*:\s*([^\{]+)\{/)[1].split(',').map(s => s.trim());
 assert.ok(inheritance.includes('ExpoReactNativeFactoryProvider'));
 assert.equal((swift.match(/factory\.startReactNative\s*\(/g) ?? []).length, 0);
 assert.equal((swift.match(/reactNativeFactory\s*=\s*factory/g) ?? []).length, 1);
 assert.equal((swift.match(/RCTLinkingManager\.application\s*\(/g) ?? []).length, 2);
});

test('scene prebuild preserves application identity, linking and unrelated plist values', async () => {
 const existing = {
  CFBundleIdentifier: 'com.primeradiant.evener.native',
  CFBundleURLTypes: [{ CFBundleURLSchemes: ['evener-native'] }],
  NSAppTransportSecurity: { NSAllowsArbitraryLoads: true },
 };
 const { plist } = await generate({ plist: existing, swift: template });
 for (const [key, value] of Object.entries(existing)) {
  assert.deepEqual(plist[key], value);
 }
 assert.ok(plist.UIApplicationSceneManifest);
});

test('repeated scene prebuild leaves generated startup and other plugin content unchanged', async () => {
 const sentinel = '\n// external_plugin_sentinel\n';
 const once = await generate({ plist: {}, swift: template + sentinel });
 assert.notEqual(once.swift, template + sentinel);
 assert.ok(once.swift.endsWith(sentinel));
 assert.deepEqual(await generate(once), once);
});
