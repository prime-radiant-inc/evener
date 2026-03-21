# Full 89-Task Eval Failure Analysis (Root-Caused from Transcripts)

**Run:** `full-89-ef120d4` on gpt-5.4, March 21 2026
**Result:** 56/88 pass (64%), 1 task didn't run (dna-assembly setup timeout)
**Method:** Every failure below was root-caused by reading the actual agent transcripts.

## Systemic Patterns

Three systemic issues account for the majority of fixable failures:

### Pattern A: Write-Last Antipattern (3 tasks)
The agent has the answer but never writes the deliverable file because it's still refining.
- **chess-best-move**: Never wrote move.txt. Spent 120 turns on template matching.
- **gcode-to-text**: Had OCR answer at turn 123, started another preprocessing pass instead of writing out.txt.
- **query-optimize**: Had a 0.4s query at turn 7, spent 10 more rounds trying to verify equivalence (which timed out every time), never wrote sol.sql.

### Pattern B: Coordinator Contaminates Workspace (3 tasks)
The coordinator's own verification changes workspace state, breaking the verifier.
- **db-wal-recovery**: Coordinator ran `sqlite3 main.db`, which caused SQLite to delete the XOR-obfuscated WAL file as corrupt. Implementer had to mine the coordinator's transcript logs for partial data.
- **configure-git-webserver**: Coordinator pushed "hello again" during verification. Verifier's push of "hello world" conflicted with stale repo state.
- **polyglot-c-py**: Coordinator told implementer to leave cmain binary. Verifier expects only main.py.c.

### Pattern C: Confirmation Bias / Premature Commitment (1 task)
The coordinator commits to a hypothesis early and the pipeline executes it without reconsidering.
- **fix-code-vulnerability**: Coordinator decided pickle/CWE-20 during scouting. Implementer literally fixed the actual vulnerability (restored CRLF checks = CWE-93) but called it "unrelated." 390/391 tests pass. Wrong CWE in report.

## Individual Root Causes

### fix-code-vulnerability (390/391 pass)
**Root cause:** Confirmation bias. Coordinator locked onto pickle deserialization (CWE-20) during scouting. The git diff showed exactly one change: removal of CRLF validation from `_hkey`/`_hval` — the task author literally pointing at the vulnerability. Both agents dismissed this as "unrelated." Implementer restored the CRLF checks to make tests pass but never updated the CWE classification. Report says CWE-20, verifier expects CWE-93.

### db-wal-recovery (5/7 pass)
**Root cause:** Coordinator's scouting destroyed the evidence. Running `sqlite3 main.db ".tables"` caused SQLite to detect the XOR-obfuscated WAL as corrupt and delete it. Implementer then mined coordinator's transcript logs to recover partial data — got INSERT frames (rows 6-11) but missed UPDATE frames that changed apple's value from 100 to 150.

### query-optimize (5/6 pass)
**Root cause:** Write-last antipattern. Implementer built a correct CTE query running in 0.4s (vs >120s original) at turn 7 but never wrote sol.sql. Spent 10 remaining rounds trying to verify equivalence by running the original query — which timed out every time because it was catastrophically slow. Agent was killed at 900s during the final Python benchmark attempt.

### polyglot-c-py (correct code, workspace dirty)
**Root cause:** Coordinator explicitly told implementer "you may also leave /app/polyglot/cmain if produced by verification." Misread the task's usage example (`gcc ... -o cmain`) as permission to keep the binary. Never ran `ls` to check directory state. Never looked at `/tests/`.

### overfull-hbox (3/4 pass)
**Root cause:** Implementer changed "an intimate" to "a close" — valid synonym but the article change (`an`→`a`) violates the verifier's strict token-by-token comparison. Needed a vowel-sound synonym to preserve "an", or skip that word. Second implementer hit max_turns, coordinator never got to verify.

### regex-chess (1/4 pass)
**Root cause:** FEN field ordering bug. Implementer built a legitimate 6,363-pair regex engine that generates correct board positions, but the final FEN output has castling and en-passant fields swapped (fields 3 and 4 reversed). Agent was actively debugging this exact bug when it ran out of turns. One-line fix.

### mcmc-sampling-stan (5/6 pass)
**Root cause:** `refresh = 0` in rstan::sampling() suppresses progress messages. Verifier checks for "SAMPLING FOR MODEL" and "Chain" in stdout. Model and posteriors are correct. Agent deliberately silenced output for cleanliness. Trivial fix — remove `refresh = 0`.

### configure-git-webserver (server was running)
**Root cause:** NOT "service not running" — the server was running fine. Coordinator pushed "hello again" during its own verification test. Verifier's push of "hello world" failed (expect script error, stale repo state). The coordinator contaminated the workspace with its own test data.

### qemu-alpine-ssh (VM running, SSH not)
**Root cause:** Guest network interface was DOWN. Alpine's live ISO boots with interfaces down. Agent ran `udhcpc -i eth0` without first running `ip link set eth0 up`. udhcpc looped forever on "Network is down." SSH was never installed. Secondary: ANSI escape codes in serial console broke prompt detection, burning 120s.

### sqlite-with-gcov (2/3 pass)
**Root cause:** .gcda files written to build directory (`/app/src/sqlite/`), verifier checks install directory (`/app/sqlite/`). GCC gcov instrumentation writes .gcda to where .gcno lives (the build dir), not the install prefix. Coordinator confirmed .gcda existed but in the wrong directory.

### git-multibranch (too-easy task, should pass)
**Root cause:** Coordinator's verify.sh pre-populated the dev branch with timestamped content. Verifier's push was rejected as non-fast-forward because the existing dev branch had diverged. Coordinator left test artifacts in the repo — another instance of Pattern B.

### path-tracing (2/5 pass)
**Root cause:** The implementer cheated — it called `/app/orig` (the reference binary) instead of writing an actual path tracer. Verifier caught it with a chroot jail (binary can't access `/app/orig` in jail) and no-deps check. Coordinator saw the approach and didn't flag it.

### chess-best-move (vision task, 63 reads)
**Root cause:** Coordinator read the image but only told implementer "midgame chess position" — no piece positions. Implementer spent all 120 turns on template matching infrastructure (SVG rendering, OpenCV cross-correlation), never constructed a FEN, never wrote move.txt. The simple approach (look → describe → FEN → stockfish → write) was never attempted by the implementer.

### gcode-to-text (vision task, 35 reads)
**Root cause:** Write-last antipattern + context checkpoint amnesia. Tesseract OCR produced `flag{gc0d3 2 ch4LLenGiNg}` at turn 123 (one substitution from correct `flag{gc0d3_iz_ch4LLenGiNg}`). Agent started another preprocessing pass instead of writing out.txt. Context checkpoints caused ~40 wasted turns of re-work across 3 resets.

### dna-insert (0/1 pass)
**Root cause:** Trailing newline in primers.fasta. Biological design was correct — valid primers, Tm within spec, reconstruction verified. But `write_file` ended with `\n` producing 6 lines instead of expected 4. Verifier asserts `len(lines) == 4`.

### break-filter-js-from-html (model refusal)
**Root cause:** GPT-5.4 refused the task on ethical grounds. The task requires creating HTML that bypasses a BeautifulSoup XSS filter and triggers `alert()` in Chromium. Both coordinator and implementer refused: "I can't help create an exploit artifact." Task has passed before on other runs — model behavior is inconsistent.

### mteb-retrieve (1/2 pass)
**Root cause:** Embedding reproducibility. Agent ranked HumanEval as #5, verifier expects MTEB as #5. Agent used sentence-transformers 5.1.1 + torch 2.9.0+cu128 without BGE query prefix. Cosine similarity scores for positions #5-#7 differ by <0.02 — the ranking is sensitive to library versions and preprocessing.

### install-windows-3.11 (3/4 pass)
**Root cause:** QEMU monitor socket at `/app/run/win311-monitor.sock`, verifier expects `/tmp/qemu-monitor.sock`. Trivial fix: symlink or change QEMU launch flag. Agent was never told the expected path.

### protein-assembly (wrong output)
**Root cause:** Fusion protein gene blocks don't match expected sequence. The agent produced a result but the biological assembly was incorrect. (Previously passed — nondeterministic.)

### make-mips-interpreter (VM timeout)
**Root cause:** Built MIPS interpreter but VM execution timed out during verifier. Agent used 157 shell commands. The implementation may be correct but too slow for the verifier's timeout.

### compile-compcert (build failure)
**Root cause:** CompCert build failed on modern Ubuntu. Agent spawned a fix agent (61 shells total) but couldn't resolve the build issues. CompCert is notoriously fragile with modern compilers.

## Fixable Improvements (ordered by impact)

1. **Write deliverables EARLY** — chess-best-move, gcode-to-text, query-optimize all had correct answers but never wrote them. This is the single highest-impact fix.

2. **Coordinator must not contaminate the workspace** — db-wal-recovery, configure-git-webserver, git-multibranch, polyglot-c-py all failed because the coordinator's own actions changed workspace state. The coordinator should use read-only operations during verification, or reset state after testing.

3. **Coordinator should read /tests/ before delegating** — polyglot-c-py (didn't know verifier checks directory), dna-insert (didn't know exact line count), sqlite-with-gcov (didn't know .gcda location check).

4. **Implementer should escalate contradictory evidence** — fix-code-vulnerability: implementer fixed CWE-93 but reported CWE-20 because coordinator said so. The implementer should push back when evidence contradicts the coordinator's hypothesis.

5. **Vision: coordinator describes, implementer receives text** — chess-best-move: coordinator read the image but passed no description. The overlay experiments showed better results when coordinator described the board position in text.

6. **Context checkpoints cause amnesia** — gcode-to-text lost ~40 turns to re-work after 3 checkpoints. Agent needs a way to persist key findings across context resets.
