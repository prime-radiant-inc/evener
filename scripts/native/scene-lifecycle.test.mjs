// Exercise the installed Expo plugin against its shipped Swift template. This
// is generated lifecycle coverage, not a check that app.json repeats a flag.
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const root = fileURLToPath(new URL('../../mobile-native/', import.meta.url));
const require = createRequire(`${root}package.json`);
const { withBuildProperties } = require('expo-build-properties');
const app = JSON.parse(readFileSync(`${root}app.json`, 'utf8')).expo;
const props = app.plugins.find(p => Array.isArray(p) && p[0] === 'expo-build-properties')[1];
const template = execFileSync('tar', ['-xOf', `${root}node_modules/expo/template.tgz`, 'package/ios/HelloWorld/AppDelegate.swift'], { encoding: 'utf8' });

async function generate() {
 const config = withBuildProperties({ ...app, _internal: { projectRoot: root } }, props);
 const apply = async (name, modResults) => {
  const mod = config.mods?.ios?.[name];
  if (!mod) return modResults;
  return (await mod({ ...config, modResults, modRequest: { projectRoot: root, platform: 'ios', modName: name } })).modResults;
 };
 return { plist: await apply('infoPlist', {}), swift: (await apply('appDelegate', { language: 'swift', contents: template })).contents };
}

test('prebuild delegates window startup to the Expo scene lifecycle', async () => {
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
