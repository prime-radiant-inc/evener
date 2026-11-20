# P1 scoping report: prebuilt pod binaries for the ios-qualification lane

Status: analysis only, no runner time used. Measured from run 44658362283
(build job 136030109993, journeys job 136038547216), the first run after the
DerivedData cache landed (#6984), plus the committed artifacts of that run
(qualification diagnostics log, build-products tarball) and the pod metadata in
`mobile-native/`.

## 1. Headline

1. **The build is dominated by compiling third-party pods, not by linking.**
   The warm `xcodebuild-release` step is 771 s of the 1248 s build job. Of the
   1076 compile steps in it, 1028 belong to pods and 11 to the app's own two
   targets. The whole link-and-package tail is under 30 s. So "link instead of
   compile" is the right lever and there is nothing to win by touching linking.
2. **Most of the tail is already serialized on a handful of targets.**
   Everything from ZXingObjC through SDWebImage finishes inside the first 67 s.
   The last 300 s of the compile phase is one chain: ReactCodegen, then the four
   community RN pods, then Expo's pods, then the app.
3. **Two working prebuilt mechanisms already exist in this graph.** React Native
   ships prebuilt React Core and React Native Dependencies as Maven tarballs and
   the lane already links them (zero CompileC for boost, glog, fmt, Folly,
   SocketRocket, React itself). Expo ships a full precompiled-modules pipeline
   that is *switched on and finding nothing*: it looks for per-pod xcframework
   tarballs in `node_modules/<pkg>/prebuilds/output/...`, and the expected paths
   are printed in the pod log. Expo also already ships tarballs for
   `expo-modules-core` and `expo-camera` inside npm, and our `package.json` tells
   Expo to ignore them (`buildFromSource`).
4. **P1's ceiling is about 8 minutes off the build job, not 20.** If every pod in
   the graph were a prebuilt binary, `xcodebuild-release` would still contain the
   ReactCodegen script phase, the app's 11 compile steps, the `[Expo] Remove
   duplicate codegen output` phase and ExpoModulesJSI, which I estimate at
   250–350 s. That puts the build job near 12–13 min from today's 20.8 min and
   the lane near 28 min from 32–36 min.
5. **Therefore P1 alone cannot reach the program target of a last iOS check
   under 10 min.** The journeys job alone measured 16 m 17 s. Even a build job of
   zero would leave the lane at ~16 min. P1 pays off only alongside a parallel
   journeys lane; on its own it buys 4–8 minutes and makes the build insensitive
   to Podfile.lock churn.
6. **Two numbers in the brief do not match what the system reports**, and I
   would rather flag them than quietly use them: the Actions cache reports
   20 entries / 8.85 GB now (not 13.79 GB / 33 rows), and the products artifact
   is 185 MB compressed (not 225 MB). Details in §7.

## 2. Where the 20 m 48 s went

Phase boundaries are the `iOS qualification: <phase>` lines of the raw job log,
which carry UTC timestamps. Differencing consecutive markers is the whole
method.

| phase | start (UTC) | duration |
|---|---|---|
| job start → source-revision (checkout, Xcode selection, cache restore) | 02:06:07.652 | 168.9 s |
| expo prebuild (first + two repeats) | 02:08:56.539 | 11.9 s |
| pod-resolve (`pod install`, first) | 02:09:08.775 | **154.5 s** |
| pod-deployment (`pod install --deployment`) | 02:11:43.295 | 31.2 s |
| generated settings, simulator runtimes | 02:12:14.521 | 11.1 s |
| simulator create | 02:12:25.618 | 0.3 s |
| **xcodebuild-release** | 02:12:25.956 | **771.0 s** |
| built-host verify | 02:25:16.992 | 2.6 s |
| absent-target-red | 02:25:19.581 | 4.5 s |
| add-ui-target | 02:25:24.048 | 1.0 s |
| build-for-testing | 02:25:25.081 | 50.8 s |
| derived-data-sizes + simulator delete + upload | 02:26:15.833 | ~40 s |
| **build job total** | | **1248.4 s (20 m 48 s)** |
| journeys job (separate runner) | 02:27:08 | **977 s (16 m 17 s)** |

Two observations that matter for every later experiment:

- The two `pod install` calls are 186 s and are *not* pod compilation. They are
  Ruby/CocoaPods work plus downloading 605 MB of DerivedData and the RN and
  Hermes tarballs. Fixing this is a different lane (it is the P2 "restore the
  Pods directory" idea), but it caps how much a build-time win is worth: P1 can
  at best halve the build job.
- `build-for-testing` is 50.8 s and produces nothing the journeys job uses
  (journeys builds its own test bundle after restoring the products artifact).
  It is in the build job only to prove the UI test target compiles. Do not touch
  it: it is a guarantee.

## 3. Where the compile work is

Counted from the qualification diagnostics log (`xcodebuild-release.log`,
54,036 lines) by counting `CompileC` and `SwiftCompile` lines per target.
Totals: **1029 CompileC + 47 SwiftCompile = 1076 compile steps**, spread over 46
targets of the 140 in the graph.

| group | CompileC | SwiftCompile | targets |
|---|---|---|---|
| RN community pods + codegen | 565 | 2 | 11 |
| CocoaPods CDN pods (C/C++/ObjC) | 366 | 0 | 4 |
| Expo modules | 97 | 35 | 29 |
| the app (`Evener`, `EvenerShareExtension`) | 1 | 10 | 2 |
| **total** | **1029** | **47** | **46** |

Per target, with the position in the build where its last compile appears
(seconds after xcodebuild starts). The second column is *not* a duration; it is
the timestamp of the target's last log line carrying an internal build-tool
timestamp, and it is only useful as an upper bound on when that target's
compiles finished:

| target | CompileC | + Swift | last emission (s) |
|---|---|---|---|
| ReactNativeEnrichedMarkdown | 167 | 1 | 600.7 |
| ZXingObjC | 163 | – | 54.4 |
| libwebp | 126 | – | 37.7 |
| RNReanimated | 125 | – | 523.9 |
| RNScreens | 91 | – | 502.6 |
| SDWebImage | 73 | – | 66.3 |
| RNWorklets | 58 | – | 502.5 |
| ReactCodegen | 42 | – | 443.8 |
| ExpoModulesCore | 40 | 1 | 544.7 |
| RNGestureHandler | 28 | – | 524.7 |
| keyboard-controller | 25 | 1 | 502.2 |
| react-native-safe-area-context | 18 | – | 502.1 |
| react-native-webview | 8 | – | 502.0 |
| Expo | 7 | 1 | ~663 |
| ExpoModulesWorklets | 7 | 1 | 651.5 |
| EXConstants | 3 | 2 | 676.2 |
| 28 single-target Expo modules (SQLite, FileSystem, ImageManipulator, Font, …) | ~40 | ~28 | 651–677 |
| Evener (app) | 1 | 7 | 759 (Ld ≈ 742) |
| EvenerShareExtension | 0 | 3 | 449.7 |

### 3.1 What the timeline supports, and what it does not

There are only 65 lines in the whole 54k-line log that carry an internal
timestamp, so I can bracket but not measure per-target wall time. The brackets:

- Everything from ZXingObjC to SDWebImage compiled inside the first ~67 s of the
  build. That is 362 of the 1029 CompileC steps (35 %) and it is already
  parallel and already cheap. A prebuilt for those pods saves at most ~66 s, and
  less once other objects are filling the cores.
- From ~74.6 s there is a ~370 s stretch with no usable timestamp anchor. The
  ReactCodegen `Libtool` step lands at line 15,667 (443.8 s), with 713 compiles
  emitted before it and 363 after. So ReactCodegen's script phase *plus* every
  compile it interleaves with occupies roughly 66 s → 444 s. Splitting that into
  "codegen script" and "compile" is not recoverable from this log.
- RNReanimated (523.9 s), RNScreens (502.6 s), RNWorklets (502.5 s),
  safe-area-context (502.1 s), RNGestureHandler (524.7 s) and
  keyboard-controller (502.2 s) all finish within a 23 s window around 500 s.
  They are the next-to-last thing to finish. Below them, ExpoModulesCore at
  544.7 s and the Expo module family at 651–677 s, then EnrichedMarkdown at
  600.7 s, then the app target's link at ~742 s.
- The link itself is small: `Ld ... Evener.app/Evener` at ~742 s, `BUILD
  SUCCEEDED` at 771.9 s, and the tail in between is plist processing, dSYM,
  Swift runtime copy, codesign and validate. **Bounded above by 30 s**, which is
  why linking is not a lever.
- The community pods' compiles cannot start before ReactCodegen's headers exist
  (`ReactCodegen` is a dependency of every one of them), which is why the first
  ~70 s belong to the CDN pods and the last ~300 s to the RN/Expo family.
  Prebuilding the community pods is what collapses the tail; prebuilding the CDN
  pods would mostly shift work off an already-parallel opening.

### 3.2 The one measurement gap

I cannot attribute wall time to a target from this log. The fix is one run with
`xcodebuild -showBuildTimingSummary` (or per-step timestamps in the diagnostics
log) added to the lane's release build. That is E0 below: no behavior change,
one run, and it turns every estimate in this report into a measurement. I am
ranking it first for that reason, not because it is fast.

## 4. The prebuilt patterns that already work in this graph

### 4.1 React Native core: default-on, Maven-hosted, links not compiles

`node_modules/react-native/scripts/react_native_pods.rb` sets both switches to
1 unless explicitly disabled:

```ruby
# Use the React Native precompiled binaries by default. Users can still turn
# them off and build from source by setting the environment variable to 0.
ENV['RCT_USE_RN_DEP'] = ENV['RCT_USE_RN_DEP'] == '0' ? '0' : '1'
ENV['RCT_USE_PREBUILT_RNCORE'] = ... == '0' ? '0' : '1'
```

The pod log confirms the fetch and the cache miss:

```
[ReactNativeCore] Using React Native Core and React Native Dependencies prebuilt versions.
[ReactNativeCore] Cache miss: downloading reactnative-core-0.88.1-release.tar.gz from
  https://repo.reactnative.dev/maven2/com/facebook/react/react-native-artifacts/0.88.1/...
```

and the same for `reactnative-dependencies-0.88.1-release.tar.gz` and
`hermes-ios-260318099.0.5-release.tar.gz`.

How the consumer is shaped (`React-Core-prebuilt.podspec`):
`vendored_frameworks = "React.xcframework"`; `source_files` is only
`Headers/**/*.{h,hpp}`; a `prepare_command` unzips `ReactNativeHeaders.xcframework`
and flattens its headers so downstream pods can `#import <React/...>`.
`ReactNativeDependencies.podspec` does the same for the prebuilt dependency
binary, and `scripts/cocoapods/rndeps_facades.rb` generates *dependency-only*
facade podspecs for RCT-Folly, glog, boost, DoubleConversion, fmt, fast_float and
SocketRocket: each has no `source_files`, declares
`ReactNativeDependencies`, and exists so 30 other podspecs' `dependency 'glog'`
lines still resolve. `Podfile.lock` shows those facades at
`build/rndeps-facades/*`, and the compile counts show zero CompileC for every
one of them.

At build time the frameworks are embedded and signed for the app target:

```
PhaseScriptExecution [CP] Embed Pods Frameworks ...
    rsync ... XCFrameworkIntermediates/React-Core-prebuilt/React.framework  Evener.app/Frameworks/
    rsync ... XCFrameworkIntermediates/ReactNativeDependencies/ReactNativeDependencies.framework ...
    rsync ... ExpoModulesJSI.framework ...
```

plus `[CP-User] Replace React Native Core ...` script phases that swap the debug
for the release framework. Two things follow, both load-bearing for the rest of
this report:

1. The app already links and embeds **dynamic** prebuilt frameworks. A prebuilt
   pod here does not have to be a static library; the embed-and-sign path is
   proven every run.
2. The template is exact and copyable: tarball in some store, podspec with
   `vendored_frameworks` + flattened headers, facades for anything that other
   podspecs depend on by name, and a flavor-switch script phase if the tarball
   is per-configuration.

### 4.2 Expo precompiled modules: pipeline enabled, no tarballs

`expo-build-properties` writes the switch (`build/ios.js:73`):

```js
EXPO_USE_PRECOMPILED_MODULES: config.ios.usePrecompiledModules ? '1' : '0'
```

with a default of `true`. Our app does not set the option, so the pipeline is on.
The pod log says it found nothing:

```
[Expo-precompiled] Precompiled modules:
[Expo-precompiled]   ⚠️  ExpoModulesCore (58.0.30) (configured by buildFromSource)
[!] [Expo-precompiled] ⚠️  Precompiled modules enabled but no xcframeworks found. All modules will build from source.
[Expo-precompiled] RNReanimated: building from source (prebuilt tarball not found)
```

and it prints the exact path it looked in:

```
/Users/runner/work/evener/evener/packages/precompile/.build/react-native-reanimated/output/
    4.7.1/0.88.1/260318099.0.5/debug/xcframeworks/RNReanimated.tar.gz
```

The same message appears for `react-native-screens`, `react-native-worklets` and
`react-native-safe-area-context`. `precompiled_modules.rb` in
`expo-modules-autolinking` is the policy: the environment gate is
`return false unless ENV[ENV_VAR] == '1'`; the search base is
`EXPO_PRECOMPILED_MODULES_PATH` if set, otherwise the package's own
`prebuilds/output`, optionally under `/<versionPrefix>`; the flavor defaults to
`debug` (`EXPO_PRECOMPILED_FLAVOR`); and
`EXPO_PRECOMPILED_MODULES_BASE_URL` exists for remote fetching with **no default
value**. When a tarball is found, `patch_spec_for_prebuilt` rewrites the podspec:
`spec.source = { :http => file://... }`, `vendored_frameworks` set,
`source_files` cleared, `DEFINES_MODULE = YES`, a `prepare_command`, and script
phases to switch flavor.

Expo's `external-configs/ios/` already describes how to prebuild exactly the pods
we care about — `@react-native-async-storage`, `react-native-reanimated`,
`react-native-safe-area-context`, `react-native-screens`, `react-native-svg`,
`react-native-worklets`, `@shopify/*` — including
`externalDependencies: [ReactNativeDependencies, React, Hermes, RNWorklets]` and
`excludeFromUmbrella`.

And npm already ships real prebuilds for the Expo modules:

| package | prebuilt payload in `node_modules` |
|---|---|
| `expo-modules-core` | 32 MB, `ExpoModulesCore.tar.gz` 14.3 MB (release) + ExpoModulesWorklets |
| `expo-camera` | 33 MB, ExpoCamera 2.8 MB, ExpoCameraBarcodeScanning 0.8 MB, `spm-deps/ZXingObjC.xcframework` |
| `expo-file-system` | 3.2 MB (release) |
| `expo-image-manipulator` | 32 MB |
| `expo-modules-jsi` | 1.5 MB |

`ExpoModulesCore.framework` from that tarball is a dynamic framework
(`file:` reports `dynamically linked shared library`), which is the same shape as
React and ReactNativeDependencies that we already embed.

The one thing standing in the way is our own config:

```json
"expo": { "autolinking": { "ios": {
  "buildFromSource": ["expo-modules-core", "expo-camera"],
  "exclude": ["evener-voice"] } } }
```

`mobile-native/src/cameraPermission.test.ts` records why `expo-camera` is on that
list: "Its precompiled framework links a dynamic ExpoCamera, but buildFromSource
(package.json) builds ExpoCamera static, and dyld stopped the app at launch."
That is a real, documented dyld failure — so a flip is not free, and it is a
journey-verified experiment, not a config edit. `expo-modules-core` has no such
note against it.

`scripts/native/check-podfile-lock.mjs` carries a related caveat in its header:
"Not handled: Expo's precompiled mode (EXPO_USE_PRECOMPILED_MODULES=1)". Worth
reading before relying on that script for any of this.

### 4.3 A hidden fixed cost: ExpoModulesJSI builds itself

`ExpoModulesCore.podspec` line 113 explains why `ExpoModulesJSI` is a
CocoaPods dependency in both modes: "ExpoModulesJSI is re-exported by
ExpoModulesCore's swiftinterface, so it must be a CocoaPods dep in both source
and prebuilt modes." Today it is produced by an in-build script phase that runs
a **nested xcodebuild**:

```
[CP-User] Build ExpoModulesJSI xcframework
[ExpoModulesJSI] Detected: prebuilt RN ...
[ExpoModulesJSI] Clearing stale build state (DerivedData, SwiftPM) before rebuild
[ExpoModulesJSI] Building framework slice for iphonesimulator...
```

The phase starts at 70.8 s and the next phase begins at 198.7 s, so it costs at
most 128 s and probably close to that, because a nested xcodebuild does not share
the outer build's parallelism. This is *not* a compile the compile-count table
can see — it hides inside a script phase. Flipping Expo to prebuilt removes it
only if real xcframeworks exist and ExpoModulesCore resolves as prebuilt
(`ExpoModulesCore.podspec` line 118 gates on
`try_link_with_prebuilt_xcframework`).

## 5. Per-pod blockers

| candidate | evidence in favour | blockers |
|---|---|---|
| `expo-modules-core` (+ ExpoModulesWorklets, ExpoModulesWorkletsAdapter) | tarball already in npm; 51 compile steps; its compiles sit at 545–677 s, i.e. on the critical tail | dynamic framework; must confirm the release tarball matches our `Podfile.lock`; `buildFromSource` entry must go; Podfile.lock changes → the lane's lock check fails once (see §8) |
| `expo-camera` | tarball in npm, 3.06 MB lib, small compile count | documented dyld failure on the precompiled path; needs a journeys run, not just a build |
| RNReanimated, RNScreens, RNWorklets, safe-area-context | 292 compile steps; last finishes ~503–524 s; Expo has external-configs for all four | no prebuilts ship; we must build and host the tarballs. Static C++/ObjC pods with `header_dir` and public headers — no resource bundles, no script phases, which is the easy case. Must be built per flavor and per lock hash |
| ReactNativeEnrichedMarkdown | 167 CompileC + 1 Swift — the single largest target; also spawns a resource-bundle target | most complex pod in the graph: Swift + ObjC++ + vendored C (md4c, highlight), vendors its own `RaTeX.xcframework`, has a `resource_bundles` entry (`RaTeXCoreFonts`, ttf files), and a second target with zero compiles. Rebuild must reproduce the bundle and embedding |
| ZXingObjC, libwebp, SDWebImage (+WebPCoder) | 362 CompileC total, pure C/C++/ObjC | all four are CocoaPods CDN trunk pods, not autolinked, so the Expo pipeline cannot see them. SDWebImage has a resource bundle. Low value anyway: all done by 67 s |
| RNGestureHandler, keyboard-controller, webview | 61 CompileC, finish ~502–525 s | mixed ObjC/C++ and Swift+codegen; same hosting problem as the community pods |
| boost, glog, fmt, Folly, SocketRocket, React, React-RCT* | already prebuilt | nothing to do; these are the reference |

## 6. What a prebuilt pod costs to produce and host

Whichever pods we prebuild, the mechanism is the same and it is not free: we need
a job that, for each `Podfile.lock` hash, builds the pod for `debug` and
`release` on the simulator SDK and uploads a tarball in the layout Expo expects
(or our own layout, if the consumer is our own podspec). Options, cheapest
first:

1. **Use npm payloads** for `expo-modules-core` and `expo-camera`. Zero
   infrastructure, zero cache budget. Only the config flip is needed.
2. **Prebuild in CI on lock-hash change and store as release assets.** Release
   assets do not count against the Actions cache budget and have no LRU churn.
   Cost: one extra macOS job amortized over the lock hash.
3. **Prebuild and store in the Actions cache** under a new prefix. Counts
   against the budget (see §7).
4. **Check the tarballs into the repo.** Rejected: hundreds of MB in git history.

## 7. Storage arithmetic

**Discrepancy first.** The brief says the cache holds 13.79 GB across 33 rows at
tonight's merge, eviction at 10 GB. `GET /repos/.../actions/cache/usage` and the
caches listing, read at 04:15 Z 2026-11-20, say **20 entries / 8,849,653,354
bytes (8.85 GB)**. Both cannot be true at the same moment. Either ~5 GB was
evicted between the merge and my read, or the 13.79 GB figure counted something
else. Do not spend that headroom until this is settled; every number below is
given against both readings.

Documented policy (`docs/developing-evener/testing.md`, "CI Caches"): steady
state 6 GB, entries evicted above 10 GB. `ci.yml`'s cache-prune job keeps the
newest entry per prefix matching
`^(go|gomod|golangci-lint|ios-deriveddata)-.*-[0-9a-f]{40}$`; a new cache kind
must be added to that regex or it will never be pruned.

Measured cost of what we would store, from the run's own build-products artifact:

- Artifact: **193,862,093 B (185 MB) compressed**, 998 files, **829 MB**
  uncompressed — 4.3× compression. (The brief says 225 MB; the artifact I pulled
  is 185 MB. Same run, so the difference is worth a look before quoting either.)
- Release-iphonesimulator static libs: **481.2 MB across 43 targets**. Top:
  RNReanimated 201.6, RNScreens 65.6, EnrichedMarkdown 41.7, ReactCodegen 37.9,
  ExpoModulesCore 30.3, RNWorklets 27.3, keyboard-controller 17.1,
  RNGestureHandler 13.2, ExpoSQLite 7.7, safe-area-context 6.1,
  ExpoFileSystem 3.9, ZXingObjC 3.9, SDWebImage 3.5, webview 3.3,
  ExpoCamera 3.1, libwebp 2.6, Expo 2.3.
- dSYMs 144.8 MB, app bundles 76.1 MB — not needed for a pod cache.

Estimates (compression ratio applied from the measured Build tree, which is what
these files are):

| scope | raw | compressed (est.) |
|---|---|---|
| `expo-modules-core` only | 30.3 MB | ~8 MB (and it is free from npm) |
| the four Expo-supported community pods | 300.6 MB | 76–160 MB |
| every heavy pod in §5 | ~430 MB | 110–200 MB |

Against the budget: **8.85 GB measured means ~1.15 GB of headroom to the 10 GB
eviction line, or ~4 GB above the documented 6 GB steady state.** One new kind
at 0.1–0.2 GB (our own tarballs) or 0 GB (npm payloads) is affordable in either
reading; a per-flavor, per-lock-hash fan-out is not — it would need
release assets or strict pruning to one entry per pod.

Comparison of carriers:

| carrier | key matching | budget | verdict |
|---|---|---|---|
| Actions cache | prefix restore-keys, LRU evicted | counts to 6/10 GB | right for lock-hash reuse **if** one entry per pod, pruned |
| workflow artifact | per-run, 90-day retention, no key matching | free of the cache budget | wrong for cross-run reuse; keep it for the build→journeys handoff it already does |
| release assets | named, no LRU | free of the cache budget | best fit for our own tarballs; needs write permission in the job |

## 8. Ranked experiments

Ranking is by expected wall-clock saving on the build job per unit of risk and
effort. "Est." figures are estimates derived from §2–§3 and labelled as such;
E0 exists to replace them with measurements. Baselines: B1 runs 44669548690 and
44669565758, whose band the coordinator is publishing; no win may be claimed
against anything but that same-day band.

| # | experiment | est. saving on build job | runs | risk / notes |
|---|---|---|---|---|
| E0 | Add `-showBuildTimingSummary` (and per-step timestamps in the diagnostics log) to the release build. No behavior change | 0 (enables everything else) | 1 | none; turns §3's brackets into per-target measurements |
| E1 | Remove `expo-modules-core` from `buildFromSource`; let Expo link the npm tarball | 60–110 s | 2 | dyld risk; app already embeds dynamic React/ExpoModulesJSI. Podfile.lock changes → first run fails at the lock check by design |
| E2 | Prebuild `ExpoModulesJSI` slice and point the pod at it, deleting the nested-xcodebuild phase | 100–128 s | 2 | needs the phase's own layout understood; obsoleted if E1 already removes it (check first) |
| E3 | Prebuild the four community pods (Reanimated, Screens, Worklets, safe-area) via Expo's external-config path | 200–280 s | 3+ | high effort: build + host tarballs per flavor and lock hash; static pods, no resources — cleanest of the big ones |
| E4 | Prebuild `ReactNativeEnrichedMarkdown` | 60–120 s | 3 | hardest pod: Swift + ObjC++ + vendored C + `RaTeXCoreFonts` resource bundle + vendored RaTeX.xcframework |
| E5 | Prebuild individual Expo module pods with npm payloads (file-system, image-manipulator, font, …) | 20–60 s | 2 | one flip per package; low risk individually |
| E6 | Prebuild the CDN pods (ZXingObjC, libwebp, SDWebImage) | 30–66 s at most | 3 | they are already parallel and already done by 67 s; low EV despite 362 compiles |
| E7 | Flip `expo-camera` to prebuilt | 30–40 s | 2 | documented dyld failure; requires journeys, and the fix may be a config choice we do not control |
| E8 | Prebuild RNGestureHandler, keyboard-controller, webview | 40–60 s | 3 | same hosting problem as E3, fewer compiles |
| E9 | Try Xcode 26's compilation cache: `COMPILATION_CACHE_ENABLE_CACHING=YES` plus a saved `CompilationCache.noindex` | unknown | 2 | CAS path is already set in the build settings, and the directory is 28 KB today, i.e. unused. Cash-compiler support for this project is unproven. Overlaps P8 |
| E10 | Restore `ModuleCache.noindex` (977 MB raw) instead of rebuilding it | unknown, likely small | 2 | 977 MB raw to save module rebuilds; overlaps P2/P8 |
| E11 | Cache `mobile-native/ios/Pods` keyed by lock hash to cut the two `pod install` calls | 120–186 s | 2 | not P1's change, and it is the single largest non-compile block; flagging it for the P2 lane |
| E12 | Investigate `[Expo] Remove duplicate codegen output` (starts at 79.8 s) and make it dependency-aware or replaceable | unknown | 1 | must not change codegen semantics |
| E13 | **REJECTED**: merge the two `pod install` calls / drop the `--deployment` one | 31 s | – | removing the second install removes the guarantee that the committed lock is what builds. A pod cache (E11) gets the same saving without weakening it |
| E14 | **REJECTED**: normalise source mtimes after checkout to make the DerivedData restore actually skip compiles | could be 300–600 s | – | checkout sets every file's mtime to "now", so normalising hides *real* source changes and would ship a stale binary. Any mtime scheme must be content-aware and belongs to P2, not here |

### Sequencing (explicit, as asked)

- **If P1 lands, P2 (cache/mtime) and P8 (xcode knobs) mostly evaporate.** P1
  removes the compile work at its source instead of persuading Xcode to skip it.
  The caveat is that naive mtime normalisation (E14) is unsafe regardless, and
  P8's compilation cache (E9) is unproven — neither is a substitute for P1.
- **P1 does not make P2 valueless.** E11 (the 186 s of `pod install`) is a P2
  win that P1 cannot touch, and it grows in relative importance as P1 shrinks the
  build job.
- **P1 cannot reach the ≤10 min target alone** (§1.5). The journeys job is
  16 m 17 s. A parallel journeys lane is a precondition for the program goal,
  whatever P1 achieves.

## 9. First implementation experiment

**E1: link ExpoModulesCore instead of compiling it.**

- Lab branch: `lab/p1-expo-core-prebuilt`, cut from `origin/main`.
- Change: in `mobile-native/package.json`, `expo.autolinking.ios.buildFromSource`
  becomes `["expo-camera"]` — `expo-modules-core` removed, nothing else touched.
  Plus, on the lab branch only, a `run-journeys` `workflow_dispatch` input
  (default `false`) that gates `ui-journeys` with an `if:`, so a build-only run
  finishes without spending a journeys runner. This scaffolding never goes to a
  product PR.
- Expected: the pod log stops printing
  `ExpoModulesCore (58.0.30) (configured by buildFromSource)`, and the compile
  count for `ExpoModulesCore` / `ExpoModulesWorklets` /
  `ExpoModulesWorkletsAdapter` goes from 51 to 0.
- **Two runs, and the first is expected to fail.** `scripts/native/ios/qualification/main.go`
  writes the committed `Podfile.lock` into `ios/`, runs `pod install`, then
  checks the lock. A prebuilt `ExpoModulesCore` changes `Podfile.lock`, so run A
  stops at `errLockMismatch` after ~8 min and leaves
  `QUALIFICATION_OUTPUT/Podfile.lock.candidate`. Run B takes that candidate into
  `mobile-native/Podfile.lock` and is the measured run: build only, then a third
  run with journeys to prove the app launches (dyld).
- Success: run B builds, and its build job beats the same-day B1 band by more
  than the band width; run C shows journeys green with the precompiled framework.

## 10. What I did not verify

- No macOS machine and no lab run. Every duration here is a *measurement of run
  44658362283* and nothing else; every saving is an estimate except where §3's
  brackets make it an upper bound.
- Per-target compile wall time is not recoverable from this log (65 timestamped
  lines in 54k). E0 fixes that.
- I did not measure how long it takes to *build* a pod tarball of our own, so the
  cost side of E3/E4/E8 is unquantified. Their savings do not depend on it, but
  their net value does.
- The dyld behaviour of the precompiled `ExpoModulesCore` is untested; the
  `expo-camera` precedent is the only evidence, and it is a failure.
- I did not check the B1 baseline logs. The brief says to ask first; I am asking.
- The 8.85 GB vs 13.79 GB cache discrepancy and the 185 MB vs 225 MB artifact
  discrepancy are unresolved.
- Whether `check-podfile-lock.mjs` (which says it does not handle Expo's
  precompiled mode) is on any path these experiments touch is unchecked.

## Appendix: how the numbers were produced

- Run metadata: `gh api repos/prime-radiant-inc/evener/actions/runs/44658362283/jobs`
  (one call) → build job 136030109993, journeys job 136038547216.
- Raw build log: `gh api repos/prime-radiant-inc/evener/actions/jobs/136030109993/logs`
  → scratch file, then `grep -n 'iOS qualification:'` for phase markers and
  their UTC stamps.
- Compile counts: count `CompileC`/`SwiftCompile` occurrences in
  `xcodebuild-release.log` from the job's diagnostics artifact, grouped by the
  target directory in each compile line.
- Sizes: `tar tzf`/`tar xzf` of the build-products artifact, `stat` on each
  `.a`, `du` for the tree.
- Cache usage: `gh api repos/prime-radiant-inc/evener/actions/cache/usage` and the
  caches listing.

## 11. E1 measured: runs A, B and C

Lane record. E1 = flip Expo's `buildFromSource` list to empty on a lab branch, fold
the resulting `Podfile.lock`, build, then run the journeys.

### Run A (44680282631) — the lock mechanism, red for the right reason

Branch `lab/p1-expo-core-prebuilt` @ `08d959090a` (one-line `buildFromSource` flip,
`-showBuildTimingSummary`, `run-journeys` dispatch gate). Build job 136101758493,
05:19:43→05:25:10Z (327 s), FAILURE:

```
CocoaPods resolution differs from the committed lock
##[error]Process completed with exit code 1.
```

immediately after `iOS qualification: pod-resolve`, before pod-deployment — the
predicted `errLockMismatch`, not an infra or resolution failure. `ui-journeys`
(136103757415) was `skipped`, which also proves the dispatch gate.

Candidate from artifact 14603953601 (85,923 B, 2,795 lines, sha256
`84aa0d8c4d2142355a501f35d9dbbdb0fe1c6b1bbbd83344b4873c3d7d2c9d90`):
`PODFILE CHECKSUM` unchanged at `7c31c3c8f2b67ab9c4be6038460369cc413fda3c`,
COCOAPODS 1.16.2, `ExpoModulesCore (58.0.30)` SPEC CHECKSUM
`71ef92defce9…` → `2581d6f690…`.

The lock diff is the cascade: five pods move to `:podspec` (ExpoModulesCore,
ExpoModulesWorklets, ExpoFileSystem, ExpoFont, ExpoImageManipulator) and three
source pods leave the graph entirely — libwebp (all subspecs), SDWebImage/Core,
SDWebImageWebPCoder, the last two bundled inside ExpoImageManipulator's prebuilt
and stripped by `strip_bundled_deps_from_spec`. expo-camera, ExpoModulesJSI and
28 other Expo modules stay source: `resolve_prebuilt_status` propagates
unavailability, so one `buildFromSource` entry had kept the whole family on
source.

### Run B (44706549207) — the mechanism held, the clock did not move

Branch @ `6aa28ba76b` (candidate lock folded in byte-identical), build-only.
Build job 136187478682, 08:56:43→09:15:11Z (1108 s), SUCCESS. Cold: `Cache not
found for input keys: ios-deriveddata-macos26-xcode-26.6-0ade225f…`.

Consumed, not fallback — pod-resolve printed `[Expo-precompiled] Precompiled
modules:` with `📦 ExpoFileSystem (58.0.13)`, `📦 ExpoFont (58.0.11)`,
`📦 ExpoImageManipulator (58.0.16)` `∟ SDWebImage.xcframework (5.21.6)`
`∟ SDWebImageWebPCoder.xcframework (0.15.0)`, `📦 ExpoModulesCore (58.0.30)`,
`📦 ExpoModulesWorklets (58.0.30)`; the base run's
`⚠️ ExpoModulesCore (58.0.30) (configured by buildFromSource)` and
`Precompiled modules enabled but no xcframeworks found` lines are gone. The four
`building from source (prebuilt tarball not found)` lines left are RNReanimated,
RNScreens, RNWorklets, safe-area-context.

Compiles: **1029/47 → 765/42 (−264 CompileC, −5 SwiftCompile)**, compiling
targets 46 → 38. Per target: libwebp −126, SDWebImage −73, ExpoModulesCore −40,
ExpoFileSystem −12, ExpoModulesWorklets −7, SDWebImageWebPCoder −4, ExpoFont −1,
ExpoImageManipulator −1. Link proof: `-framework ExpoModulesCore/ExpoFileSystem/
ExpoFont/ExpoImageManipulator/ExpoModulesWorklets/ExpoModulesJSI` and
`[CP] Embed Pods Frameworks` rsyncing four new `.framework`s into
`Evener.app/Frameworks`; `** BUILD SUCCEEDED **`.

Phases, marker to marker: setup 154.2 → pod-resolve 122.9 → pod-deployment 30.7
→ settings 9.1 → **xcodebuild-release 668.8** → build-for-testing 54.9.
`Build Timing Summary`: CompileC 765 tasks/1192.357 s, PhaseScriptExecution
58/159.561 s, SwiftCompile 29/79.914 s, SwiftDriver 27/72.220 s, ScanDependencies
349/57.641 s.

### Run C (44714495859) — the runtime surface held, sample #2 is slower

Same branch, `run-journeys=true`, dispatched 09:53:17Z.

Build job 136214292405, 09:53:22→10:18:35Z (1513 s, 25m13s), SUCCESS, also cold.
Phases: setup 183.4 → pod-resolve 163.1 → pod-deployment 43.6 → settings 13.4 →
**xcodebuild-release 923.8** → build-for-testing 87.8. DerivedData: Build 2.0 G,
Products 806 M, ModuleCache 827 M.

Journeys job 136226604809, 10:18:38→10:34:11Z (15m33s), SUCCESS — inside the
15m03–18m56s band, at its fast end. `ui-journey` phase 10:20:36→10:33:50Z
(13m14s), of which xcodebuild test execution was 347.4 s:

```
==> xcodebuild exit: 0
==> journey testSavedHubMarkdownAndDraftRecovery passed
==> journey testSheetGestureKeepsControlsUsable passed
==> journey testWarmAndColdShareSchemeActivation passed
==> all three qualification journeys passed

Executed 3 tests, with 0 failures (0 unexpected) in 347.398 (347.404) seconds
testSavedHubMarkdownAndDraftRecovery  282.485 s
testSheetGestureKeepsControlsUsable    44.379 s
testWarmAndColdShareSchemeActivation   20.535 s
```

Dyld: the app launched four times (`Launch com.primeradiant.evener.native`) and
no line in the job log or the artifact matches `Library not loaded`, `dyld:`,
`Symbol not found` or `image not found`. The bundle in the products artifact is
the one the build job produced with the four `.framework`s embedded; a missing or
mis-signed dynamic framework aborts at launch and would have failed every
journey. Three passing journeys is the dyld proof for ExpoModulesCore,
ExpoFileSystem, ExpoFont and ExpoImageManipulator.

Webp: **not proven.** `grep -c -iE "webp"` and `grep -c -iE "image|photo"` on
`scripts/native/ios/EvenerQualificationUITests.swift` both return 0, and no log
in the artifact mentions webp. The SDWebImage/libwebp code now lives inside
ExpoImageManipulator's xcframework and links into the app, but no journey decodes
a webp image, so run C says nothing about a decode path regression.

#5992 corpus: first launch → automation session 4.59 s → 30.77 s (**26.2 s**),
second launch 224.75 s → 227.09 s (**2.3 s**), third and fourth < 0.5 s. The
26.2 s sits inside #5992's documented 19–60 s first-launch band for *passing*
runs, and the 2.3 s second launch sits inside its "later launches ~4 s" range.
Run C is therefore a confirming datum for that issue, not a new pathology — and
it is not a #5992 failure.

### Verdict

- Mechanism: **verified win.** Five pods consumed as prebuilt xcframeworks, three
  source pods out of the graph, −264 CompileC, app links and embeds them, app
  launches, all three journeys pass.
- Wall clock: **not proven, and no longer looks favourable.** Run B 668.8 s sits
  inside the 583.0–823.7 s B1 band; run C 923.8 s is above it.
- Normalised by box speed (pod-install time as the proxy — B1's own slow/fast
  ratios are 1.413 for xcodebuild and 1.410 for pod install, so box speed scales
  both), xcodebuild/pod-install is 4.046 and 4.054 for the two B1 controls and
  4.354 and 4.469 for runs B and C. Both variant samples are ~7–10 % worse per
  unit of box speed. Caveat: the proxy is unvalidated and two samples are not a
  band; an interleaved same-day control run is the only clean settlement.
- The 264 removed compiles were running in parallel with, and were never on, the
  critical path. What remains sets it: ReactNativeEnrichedMarkdown 167,
  ZXingObjC 163, RNReanimated 125, RNScreens 91, RNWorklets 58, ReactCodegen 42,
  and ExpoModulesJSI's ~128 s nested-build script phase.

### Decision

Do not claim a duration win for E1. Land the mechanism only if the product PR can
carry it on its merits (fewer compiles, smaller build graph), and spend the next
runs on the four RN pods still building from source (RNReanimated, RNScreens,
RNWorklets, safe-area-context) and on ExpoModulesJSI's script phase, which is
where the critical path actually is. Journeys jobs also spend ~6m20s of their
13m14s phase outside the tests (runtime pair build, hub/provider start, simulator
boot) — a separate lever from anything in this report.
