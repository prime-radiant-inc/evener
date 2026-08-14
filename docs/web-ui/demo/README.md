# Before/after demo movie pipeline

Built with the proving-it-works-with-a-movie skill (stills route). To
rebuild: capture the shots named in scenes.yaml (old UI = a worktree at
0d9b0fb23 running `vite --port 5198`, new UI = current main on 5199; the
galleries at /dev/widgets plus the live app), render card-title/card-end
via a browser screenshot at 1920x1080, then:

    narrate scenes.yaml narration/
    assemble scenes.yaml silent-cut.mp4
    make-subtitles narration/manifest.json movie.srt --offsets-json segments/offsets.json
    burn-subtitles silent-cut.mp4 movie.srt movie.mp4
    check-movie movie.mp4   # then LOOK at the contact sheet

Screenshots are not committed (they contain live session data and are
regenerable); scenes.yaml is the source of truth for shots + narration.
