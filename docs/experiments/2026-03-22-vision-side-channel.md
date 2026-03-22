# Vision Side-Channel: The Fix for GPT-5.4 Vision in Agent Pipelines

**Date:** March 22, 2026
**Result:** chess-best-move 0/3 → 6/6 correct
**Branch:** fix-explorer-model (commit 134f4e9)

## The Problem

GPT-5.4 reads chess boards perfectly in direct API calls (25/25 pieces correct,
finds mate-in-one). But in the agent pipeline with tools available, it never
uses native vision — it writes PIL code, downloads SVG templates, clones repos,
installs ML models, and times out. 18 runs across 7 experiments, 14 failures,
all following the same template-matching rabbit hole pattern.

## Why Prompts Don't Work

Tested: "trust what you see", "describe before coding", "don't write code for
perception", "do the work then verify". ALL ignored. GPT-5.4 in tool-calling
mode is trained to emit tool calls. When tools are available and an image is
in context, the model will call exec_command to write analysis code 100% of
the time, regardless of system prompt instructions.

## Why tool_choice=none Doesn't Work

Setting tool_choice="none" for one turn after image reads eliminates the
template matching rabbit hole (52 lines vs 100+). But the model produces
**empty text** — it has nothing to say without tools. Then on the next turn
(tools restored), it hallucinates a famous training position (Scholar's Mate)
instead of reading the actual board. 0/3 with this approach.

## The Solution: Vision Side-Channel

**Architecture:**
1. Agent calls `read_file("chess_board.png", purpose="identify all chess pieces and their positions")`
2. read_file returns the image as usual (ImageResult with raw bytes)
3. After tool results are assembled, if any contain image data:
   a. Extract the `purpose` from the ImageResult
   b. Make a SEPARATE API call with the image + purpose + NO TOOLS
   c. The model describes what it sees (native vision, forced text)
   d. Inject the description as a steering message
4. Agent's next turn sees the description in its conversation context
5. Agent uses the text description for FEN construction, Stockfish, etc.

**Key design decisions:**
- The calling LLM provides the `purpose` via a new read_file parameter
- The side-channel prompt is the LLM's purpose + a generic suffix
- Explorer agents skip the side-channel (just inventorying files)
- detail:"original" on the side-channel call for maximum vision quality

## Root Cause Analysis: All 18 Chess Runs

### Failure categories (14 failures)

| Category | Count | Description |
|----------|-------|-------------|
| A: Template matching rabbit hole | 12 | Downloaded SVGs, installed cairosvg, cloned lichess repo, ran template matching. Never constructed a FEN. |
| A+B: Template matching + wrong FEN | 1 | Reached Stockfish but on wrong position (king misplaced) |
| B: Vision hallucination | 1 | Saw 4 pieces instead of 25 at detail:auto |

### What made the 4 pre-side-channel passes work

All passes used lightweight piece identification (brightness stats, silhouettes)
and committed to a FEN early. Then Stockfish found mate-in-1, giving high
confidence to write the file. The critical fork: after identifying occupied
squares (all runs do this correctly), the model either (a) commits to a FEN
from vision + stats → PASS, or (b) tries template matching → FAIL. Stochastic,
~25% probability of (a).

### What makes the side-channel work 100%

The side-channel removes the stochastic fork entirely. The model ALWAYS gets
a detailed text description of the board before it has any tools to call.
With the description in context, it constructs the FEN from text (no vision
needed for subsequent turns), validates with python-chess, runs Stockfish,
and writes move.txt. The description is accurate because the side-channel
call has no tools — the model uses its native vision, which is perfect at
medium+ effort with detail:original.

## Code Changes

### session.go
- `describeImage()`: Makes side-channel API call after image tool results
- Uses `r.ImagePurpose` as the vision prompt (from read_file's purpose param)
- Injects description via `s.Steer()`
- Skips explorer agents
- Removed tool_choice=none experiment code

### tool_registry.go
- `ImageResult.Purpose`: New field carrying the caller's purpose
- `ToolExecResult.ImagePurpose`: Passed through to session

### profile.go
- `defReadFile()`: Added `purpose` parameter to tool definition
- Description updated to mention the system will provide descriptions
- `WithModel()`: Resolves provider/model strings (e.g. "openai/gpt-5.4-mini")
- Cross-provider resolution (OpenAI profile → Anthropic profile if needed)

### openai/adapter.go
- `defaultImageDetail()`: Returns "original" for GPT-5.4+, "high" for older
- Applied to both tool result images and user message images

### openaicompat/adapter.go
- Added image support to tool results (was completely missing)

### core.md
- Vision section rewritten: no read_file mention
- Added "do the work, then verify" workflow guidance

## Experiment Log

| # | Experiment | chess-best-move | Key observation |
|---|-----------|----------------|-----------------|
| 1 | fix-vision-section (prompt) | 0/3 | Removed read_file trap, neutral alone |
| 2 | fix-detail-high | 1/3 | Better image quality helped 1 rep |
| 3 | fix-explorer-model | 1/3 | Working explorer, same 1/3 rate |
| 4 | Combined (1+2+3) | 1/3 | No stacking effect |
| 5 | + write-first prompt | 0/3 | "Do the work" = template matching to the model |
| 6 | + trust-vision prompt | 1/3 | Prompt completely ignored |
| 7 | + tool_choice=none | 0/3 | Empty text + Scholar's Mate hallucination |
| 8 | Direct API (no tools) | 5/5 correct | Model vision is perfect without tools |
| 9 | Side-channel v1 (specific suffix) | **3/3** | LLM-driven purpose works |
| 10 | Side-channel v2 (generic suffix) | **3/3** | Still works with minimal suffix |

## Regression Risk

The side-channel adds an extra API call per image read (non-explorer). This:
- Increases latency (~10-30s per image)
- Increases cost (~500-2000 tokens per description)
- Could confuse non-vision tasks if triggered on irrelevant images

Needs regression testing on the full 8-task regression set before shipping.
