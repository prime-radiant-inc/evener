# Improvements-tour movie

A narrated tour of the web UI redesign in which every interactive scene is
also a live end-to-end test: record.mjs drives a real browser against the
running hub, and a scene's frames exist only if its assert_ steps passed
during recording. The final full pass ran all eight motion scenes green in
one continuous take (spawn → slash completion → /goal → /model → unknown
slash → palette handoff → ask/answer → shutdown+delete).

Rebuild:

    ln -s <capture-workspace> shots     # captures are regenerable, not committed
    node record.mjs scenes-motion.yaml shots/
    ./make-effects pan <tall.png> shots/surfaces-pan 72       # see scenes.yaml
    ./make-effects dissolve <dark.png> <light.png> shots/theme-dissolve 40
    # title/end cards: screenshot card-*.html at 1920x1080 (assemble's own
    # card renderer produced Chrome error pages on this box, twice)
    narrate scenes.yaml narration/
    assemble scenes.yaml silent-cut.mp4
    make-subtitles narration/manifest.json movie.srt --offsets-json segments/offsets.json
    burn-subtitles silent-cut.mp4 movie.srt movie.mp4
    check-movie movie.mp4     # then LOOK at the contact sheet

Gotchas learned the hard way: enterToSend defaults off (send is Mod+Enter);
a pending ask hides the composer, so ask scenes must resolve before typing;
dormant daemons idle out - spawn with a one-line prompt when a scene needs
a session that must still exist minutes later; and never re-run a scene
whose footage you want to keep (a scene start wipes its frames dir).
