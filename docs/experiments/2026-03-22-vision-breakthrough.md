# Vision Breakthrough: The Vision Prompt Causes Vision Failure

**Date:** March 22, 2026
**Discovery:** The `## Vision` section in core.md is the direct cause of GPT-5.4
failing to use its vision capabilities in the agent pipeline.

## The Definitive Test

Six API calls to GPT-5.4 with the same chess board image and same prompt
("list every occupied square"), progressively adding serf infrastructure:

| Test | System prompt | Tools | Result |
|------|--------------|-------|--------|
| 1. No system, no tools | none | none | **PERFECT board reading** |
| 2. core.md, no tools | core.md | none | **PERFECT board reading** |
| 3. core.md + coordinator.md, no tools | both | none | **PERFECT board reading** |
| 4. No system, WITH tools | none | read_file, exec_command, write_file | **PERFECT board reading** |
| 5. core.md, WITH tools | core.md | same tools | **BROKEN — calls write_file("tasklist.txt")** |
| 6. core.md + task prompt, WITH tools | core.md | same tools | **BROKEN — calls exec_command("ls")** |

Then bisected which section of core.md causes the failure:

| Test | System prompt sections | Tools | Result |
|------|----------------------|-------|--------|
| A. Identity only | Identity | yes | **PERFECT** |
| B. Identity + Values | Identity + Values | yes | **PERFECT** |
| C. Identity + Vision | Identity + Vision | yes | **BROKEN — calls read_file** |
| D. Identity + communicate | Identity + communicate | yes | **PERFECT** |
| E. Full core.md | all sections | yes | **BROKEN — calls read_file** |

**The Vision section is the sole cause.** Specifically, the text:
"Calling `read_file` on an image (PNG, JPG, BMP, GIF) sends the image to you visually"

When the model sees this instruction AND has tools available AND receives an image
in the user message, it decides it should call `read_file` instead of analyzing the
image that's already in front of it. The instruction meant to help the model use
vision is the thing preventing it from using vision.

## Why This Happens

GPT-5.4 in tool-calling mode has a strong preference for emitting tool calls over
text. When the Vision section mentions `read_file`, the model associates image
analysis with calling that tool. Even though the image is already in the conversation,
the model "knows" it should use `read_file` for images because the system prompt
says so. So it calls `read_file` on the image file (or starts writing PIL code to
process it) instead of just looking at what's already there.

## What GPT-5.4 Actually Sees

When the Vision section is removed, GPT-5.4 reads this specific 640x640 chess board
**perfectly**:

```
a8=black rook, c8=black bishop, d8=black queen, f8=black rook,
b7=black pawn, f7=black pawn, g7=black pawn,
a6=black pawn, c6=black knight, e6=black pawn,
d5=black knight, e5=white pawn, f5=black king, g5=black bishop, h5=white pawn,
a3=white pawn, c3=white knight,
b2=white pawn, e2=white queen, f2=white pawn, g2=white pawn,
a1=white rook, c1=white bishop, e1=white king, h1=white rook
```

This is correct for every single piece. The model's vision is accurate — the
framework is preventing it from being used.

When asked to find checkmate-in-one, it correctly identifies **Qe4#** (e2e4),
which is one of the two correct answers.

## Implications

1. **The Vision section must be rewritten** to not mention `read_file` in connection
   with images. It should say something about describing what you see when images
   appear in your context, without suggesting a tool to call.

2. **The `read_file` tool description already says it handles images.** The tool
   definition in profile.go says: "For image files (PNG, JPEG, GIF, WebP, BMP),
   returns the image for visual inspection." This is sufficient — the model knows
   `read_file` can show images without the system prompt reinforcing it.

3. **The agent needs to use `read_file` when it doesn't already have the image.**
   The coordinator/implementer won't have the image in their context — it's in a
   file on disk. They DO need to call `read_file` to see it. The problem is that
   the Vision section makes them think `read_file` is for ANALYSIS rather than
   just VIEWING.

4. **The fix should focus on what happens AFTER the model sees an image** (via any
   means — user message or read_file result). The instruction should be about
   describing what you see, not about how to access images.

## Proposed Fix

Remove or rewrite the Vision section. Options:

**Option A: Remove entirely.** The read_file tool description already says it handles
images. The model will call read_file on PNG files naturally and see them. No special
instruction needed.

**Option B: Focus on post-viewing behavior only.**
```
## Vision

You can see images. When an image appears in your context — whether from a user
message or a read_file result — describe what you see alongside your next action.
If you need more detail, crop or zoom specific areas, read_file those crops, and
describe what you see in each one.
```

This avoids mentioning read_file as the WAY to see images (which triggers the
"call read_file instead of looking" behavior) and focuses on what to do AFTER
seeing one.

**Option C: Minimal.**
```
## Vision

You can see images. When you see one, describe what's in it.
```

## Research Context

- **OpenAI docs:** Recommend `detail: "original"` for spatial tasks on GPT-5.4+.
  We should check if serf sends this parameter.
- **Anthropic docs:** Explicitly note chess piece positions are hard. Recommend
  "describe every data point you see" before answering, step-by-step thinking,
  and temperature=0.
- **SVRepair paper:** "Progressively narrow input to bug-centered regions" —
  the crop-zoom cycle is validated in the literature.
- **No major agent harness** has sophisticated vision prompting. They all just
  pipe images and let the LLM handle it — which is actually the right approach
  given our findings.

## Local Test Results (v2-v8 prompt variants)

| Variant | Wrote file? | Correct? | Vision reads | PIL code | Notes |
|---------|------------|----------|-------------|----------|-------|
| v2 "describe to best of ability" | 0/5 | - | many | many | Went to PIL |
| v3 "go through systematically" | 0/5 | - | many | many | Went to PIL |
| v4 "not code — text" | 2/5 | 0/2 | few | 0 | Described but hallucinated |
| v5 "crop and zoom" | 0/1 | - | 79 | 7 | Right workflow, no descriptions |
| v6 "describe + crop iterate" | 0/1 | - | 79 | 8 | Same |
| v7 "strict sequence" | 0/1 | - | 40 | 8 | 19 text blocks! But bare-text nudge fights it |
| v8 "describe alongside tool call" | 5/5 | 0/5 | 18 | 0 | Best workflow but hallucinates position |

**v4 and v8 changed the behavior but the position readings were wrong because the
Vision section was still in core.md, causing the model to not fully engage its vision.**

## What To Do Next

1. Try Option B or C (rewritten Vision section) with the direct API test to confirm
   it doesn't break the perfect reading from tests 1-4.
2. Run the chess task through serf with the fixed Vision section.
3. If the model reads the board correctly in serf, test at scale (3+ reps on AWS).
4. Check the `detail` parameter — serf may be sending images at lower resolution
   than the direct API test.
