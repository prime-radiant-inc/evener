# Native plugin acceptance

## Owned local fixture

On 6 September 2026, used the isolated SecondHub runtime on port 56501 through
its existing port 9200 proxy. No production hub or credentials were touched.
`mobile-native/scripts/plugin-fixture.mts setup` creates a temporary directory
marketplace with one manifest-only plugin, registers it and installs it through
the real native wire client and hub handlers. It refuses an existing marketplace
with the same name. `status` reads the installed list without changing it.

Fixture source: `/var/folders/46/dz2z92w907j150sqxn8b8y1c0000gn/T/evener-mobile-plugin-kUmrsL`.
Marketplace: `native-mobile-fixture`; plugin: `native-tools`, version `1.0.0`.
No hooks, MCP servers or executable content are present. The marketplace and
source remain for subsequent native Browse/install testing; the installed plugin
was removed at the end of this pass.

## Installed lifecycle on iOS and Android

Both Release apps from `b23680c19` were already on Plugins. Installing the fixture
through the script caused both lists to show native-tools without reopening.
Opened detail on iPhone 17 Pro and Pixel 7 API 35.

- Disabled on iOS; independent wire status reported enabled false.
- Enabled automatic upgrades on Android; iOS's open detail switch changed to on.
- Re-enabled on Android; iOS changed to on. Independent wire status reported
  enabled true and autoUpgrade true.
- Tapped Upgrade on each platform; both displayed Checked for upgrades. This
  directory-backed source does not establish fetching a newer Git revision or
  background automatic upgrade behavior.
- Expanded installation details on Android. The full path wrapped within the
  viewport, with Remove still reachable.
- Removed on Android through the native confirmation showing plugin, marketplace
  and hub. Android returned to empty; iOS's open detail closed and its list became
  empty after the notification. Independent wire status returned an empty list.

Both detail screenshots were captured and visually inspected. The screen is
functional, but visual acceptance remains open: detail hierarchy and destructive
action styling need refinement; the hub's selectable header also shows a gray
selection highlight on Android in this capture.

TypeScript and fixture-script Biome pass. The previous screen commit's 258 native
tests and both Release builds remain the automated baseline; this pass added a
manual fixture script and evidence, not app code. Marketplace browsing/install,
Git-backed upgrade, filtering/duplicate identities with realistic lists, failure
and disconnect paths, large text, screen readers and physical-device validation
remain. This evidence does not close MOB-014.

![iOS installed plugin detail](assets/plugins/ios-detail.jpg)
![Android installed plugin details expanded](assets/plugins/android-detail.png)
