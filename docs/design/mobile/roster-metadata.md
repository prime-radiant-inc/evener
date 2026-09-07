# Session-row metadata

The native roster now shares the web rail's state vocabulary. Quiet rows omit
idle/notLoaded noise; working, your move, question waiting, warning and failed
remain visible. A pending question alongside another state retains both signals.
Signals wrap independently of the project path, so path truncation cannot hide
an outstanding decision. Session ordering and navigation are unchanged.

Project paths retain their full source value. At ordinary text sizes they use one
line with middle truncation; above font scale 1.4 they use two lines with tail
truncation. React Native documents tail as the supported Android multiline mode:
[Text ellipsizeMode](https://reactnative.dev/docs/0.86/text#ellipsizemode).
The full path and humanized state are also supplied as the row's accessibility
hint. This is not a new visual full-path inspector.

## Native evidence

Both final Release builds connected directly to the Native E2E hub and opened
Project page fixture 1 from the roster, then returned successfully. No session
messages were sent during this pass.

- [Android before](assets/roster-metadata/android-before.png) and
  [after](assets/roster-metadata/android-after.png).
- [iOS before](assets/roster-metadata/ios-before.jpg) and
  [after](assets/roster-metadata/ios-after.jpg).
- [iOS largest-text scrolled roster](assets/roster-metadata/ios-large.jpg).

The second Android row measured 241 physical pixels before and 191 after, with
unchanged text sizes and touch minimums. Its redundant raw state previously
forced metadata onto a second line. The first row stayed 191 pixels high.
The fixture paths fit at normal size, so these screenshots do not exercise
middle truncation of a longer path. iOS largest text shows the two-line path
limit. iOS accessibility inspection exposed the full fixture path and idle
state in the row's help metadata; this is not a complete VoiceOver pass.

The Android 2x inspection timed out and a screenshot showed a connection error.
The direct hub remained listening. Restoring normal text recreated the activity
and the next inspection showed Connected with the roster restored, without a
manual reconnect. The cause is not established; this pass does not qualify
Android large-text metadata or reconnect reliability. Both text settings were
restored and read back (iOS large, Android 1.0).

Both final Release builds, native TypeScript/Biome, all 349 native tests and
make test-web passed. The existing 172 RailRow tests also passed after extracting
the unchanged state-label function. Independent source review found the initial
risk of truncating a question signal; separating signals from paths addressed
that finding, with no further concrete regression reported. Native mixed-state
fixtures, full screen-reader operation, long paths and small devices remain open.
