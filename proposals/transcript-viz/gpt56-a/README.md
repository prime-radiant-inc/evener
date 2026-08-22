# Transcript-comprehension visualization proposals

All three mockups use the same synthetic long session: 286 durable turns, 1,842 tool calls, nine delegates across three levels, five background jobs, three compactions, a provider error/retry, and a runaway watch episode.

## Idea 1 — Tempo Atlas

**Comprehension question:** Where did the wall-clock time go, and was a quiet or frantic interval healthy? The atlas uses a true wall-clock x-axis with actor lanes for main, nested delegates, jobs, and watches; event density becomes a seismograph-like tempo trace, while compactions and failures cut vertically across every lane. This scales because thousands of calls aggregate into minute-scale bins without hiding actor concurrency, and an alternate turn-index mode exposes when a short transcript span consumed disproportionate time. Hover reveals a minute and lane, clicking selects a work phase, and consequential moments act as jump targets into the transcript.

## Idea 2 — Context Current

**Comprehension question:** What evidence reached the main agent, what stayed inside a delegate, and what survived each context reset? Provider-bound context is a widening river; delegated sessions are tributaries with explicit spawn and delivery confluences, shell jobs return as evidence capsules, and compaction boundaries are narrow amber waterfalls that visibly reduce the current. It scales by showing causally meaningful flows and memory regimes rather than every tool-result body, while still annotating the session’s full counts, depth-three tree, and interrupts. Hover inspects any stream or event, click pins its provenance and cost, and a toggle switches ribbon thickness from context mass to constant wall-time width.

## Idea 3 — Session Score

**Comprehension question:** What repeating work rhythm characterized the session, and exactly where did that rhythm break? Every durable turn is one beat in a score of 24 twelve-turn measures; assistant latency is a stem, tool volume a cyan stack, long job waits a rest, delegate boundaries diamonds, compactions barlines, and failures sharp red accents. The fixed turn grid honestly keeps all 286 turns on screen and remains legible as sessions grow by adding measures vertically, while measure-level zoom can recover transcript detail. Hover reads a beat, click pins its turn-level evidence, and filters isolate latency, ensemble work, or faults without changing positions.
