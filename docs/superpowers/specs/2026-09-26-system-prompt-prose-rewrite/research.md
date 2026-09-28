# System prompts for coding agents: evidence review for the Evener rewrite

Prepared 2026-09-26. Sources are numbered [1] to [51] and listed in full at the end with title, authors or organization, date and URL.

Evidence labels:
- Controlled: an experiment with a stated manipulation and reported effect sizes.
- Vendor: official guidance from a model provider, usually backed by internal evals the vendor does not publish.
- Practitioner: blog posts, analyses of real prompts, experience reports.

Model generations matter. Many controlled studies test 2024-era models (GPT-4o, Claude 3.5, Llama 3, Gemma). I flag where a result may not carry over to Opus 5.5, Opus 5, Sonnet 5, GPT-5.5/5.6/6, GLM-5.3 or DeepSeek V4.1. I used more sources than the 15-25 target because the six questions draw on separate literatures; the main conclusions rest on about twenty of them.

Bottom line, in six points:
1. Adherence falls as rules are added, and a large share of conditional-rule failures (over 30% in the one agentic benchmark) come from the agent not noticing that the condition fired. Both OpenAI and Anthropic now tell developers to cut prompts down, and OpenAI reports leaner prompts scored about 10 to 15% higher on its internal coding-agent evals [11].
2. Your "rules are inert at decision time" finding matches a documented knowing-doing gap. The best-supported fixes are mechanisms and reminders delivered at the moment of the decision. Concrete trigger-plus-act phrasing has thinner but positive evidence.
3. Style transfer from prompt to output has vendor support and indirect controlled support. Nobody has measured whether a system prompt's own tics (em dashes, "X, not Y", bold labels) raise those tics in a current frontier agent's output or reasoning. Evener can measure it cheaply.
4. Vendors agree on the rest: plain direct language, emphasis only for true invariants, reasons stated, positive examples, contradictions resolved in the text. They disagree on details by model, and GLM and DeepSeek publish no prompt-style guidance.
5. At 15 tasks with one run per arm, a single model's score has to move by roughly 25 to 35 points before the change clears run-to-run noise. "Rules showed no measurable benefit" is partly a statistical power problem.
6. Two cautions for the rewrite: Claude Opus 5 over-verifies when told to verify, so pre-flight acts should improve an existing check and avoid adding new steps; and the flash models are the most sensitive to rule density and phrasing, so test cuts there first.

## 1. Findings table

| Question | Finding | Evidence strength | Sources |
|---|---|---|---|
| Q1 Style transfer | The formatting style of the prompt carries into Claude's output formatting; Anthropic recommends matching prompt style to the output you want. | Vendor | [1] |
| Q1 | Models converge toward the style of their context, often more than humans do; instruction-tuned and larger models converge less, and a 70B tuned model sat near human levels. | Controlled, open-weight 1B to 70B, dialogue context | [17] |
| Q1 | Input register changes output length and density: engagement-seeking phrasing produced replies up to 90% longer. | Controlled, three open-weight models | [20] |
| Q1 | Tuned models keep a trained house style across genres; em dashes survive explicit prohibition on GPT-4.1 but disappear on Claude Opus 4.6. Tics look largely trained in, and removability is model-specific. | Controlled (one PNAS paper, one preprint) | [18] [19] |
| Q1 | Explicit instructions about reasoning traces are followed under 25% of the time by open reasoning models, so prompt control of reasoning style is weak. | Controlled | [21] |
| Q1 | A model's own earlier errors in context raise its later error rate; context patterns propagate within a transcript. | Controlled (ICLR 2026) plus practitioner | [22] [35] |
| Q1 | No controlled study tests whether a system prompt's own tics raise those tics in agent output or reasoning. | Gap | none |
| Q2 Density | Success on all instructions falls steadily with instruction count; count alone predicts it with about 10% error. | Controlled, 2024 models | [23] |
| Q2 | Top reasoning models hold simple instructions near-perfectly up to about 150, then decline; smaller models decay early; earlier instructions are favored. | Controlled, 2025 models, keyword tasks | [24] |
| Q2 | On real agent system prompts (1,723 words, 11.9 constraints on average) the best model met 59.8% of constraints; over 30% of conditional-rule errors were missed triggers. | Controlled, 2024-25 models | [25] |
| Q2 | Tension between instructions explains part of the decline with count. | Controlled | [26] |
| Q2 | System-prompt adherence drifts over long dialogues; re-inserting the prompt before turns reduces drift. | Controlled, old models | [27] |
| Q2 | Vendor: a rule that is present but ignored usually means the file is too long; leaner prompts raised internal coding-agent eval scores about 10 to 15% and cut tokens 41 to 66%. | Vendor | [5] [11] |
| Q3 Stated vs applied | Models state correct rules or rationales and then act against them, and the slip leans toward the model's trained default. | Controlled (one older model, one narrow preprint) | [29] [30] |
| Q3 | An agent's explanation of its own decision is weak evidence: reasoning models mention hints they used often under 20% of the time. | Controlled | [31] |
| Q3 | Rewriting a rule as a concrete trigger with a fixed action halved misapplication (13.9% to 6.8%). | Controlled, narrow, confounded with emphasis | [30] |
| Q3 | Guidance retrieved for the agent's current state beats a static list of lessons; the agent's own successful trajectories as examples beat a model upgrade. | Controlled, 2024-25 models | [32] [33] |
| Q3 | Mechanisms move behavior: a linter-gated edit tool added 3 points on SWE-bench Lite; harness reminders after tool results halved silent stretches on Opus 5.5; hooks are deterministic where prompt rules are advisory. | Controlled plus vendor | [34] [4] [5] [6] |
| Q3 | Counter-evidence: detailed playbooks of domain strategies beat concise summaries. | Controlled | [36] |
| Q4 Phrasing | Politeness, commands, threats and tips have no reliable aggregate effect on accuracy; per-question effects are large and unpredictable. | Controlled, GPT-4o era | [37] [38] |
| Q4 | Newer Claude and GPT models overreact to emphatic wording (overtriggering, excess tool calls); reserve ALWAYS/NEVER for true invariants and emphasize at most one line. | Vendor, consistent across Anthropic and OpenAI | [1] [5] [9] [10] [14] |
| Q4 | State what to do and show positive examples. Exception: a list of specific, observable patterns to avoid works on Opus 5.5, while naming a token to suppress backfired on Opus 5. | Vendor, mixed | [1] [2] [3] [4] |
| Q4 | Negation comprehension improves with scale and is fragile in small models. No controlled test of "don't X" against "do Y" rules in agent prompts on current models. | Controlled (comprehension only) | [39] [40] |
| Q4 | Giving the reason behind a rule is vendor-recommended; I found no controlled prompt-level test. | Vendor | [1] [8] |
| Q4 | Contradictory rules cost reasoning tokens, and models resolve them silently: strong models detect conflicts when asked but rarely flag them. | Vendor plus controlled | [9] [10] [41] |
| Q5 Vendors | Anthropic and OpenAI converge: smallest prompt that covers the contract, each rule stated once, legacy scaffolding removed, outcome and success criteria over step lists, re-tuned per model. | Vendor | [1] to [14] |
| Q5 | Model-specific: Opus 5 over-verifies if told to verify; Sonnet 5 applies instructions only to what they name; GPT-5.5/5.6 want outcome-first prompts; GPT-6 Astra formats heavily and asks more clarifying questions; GLM and DeepSeek publish no prompt-style guidance. | Vendor | [2] [3] [10] [11] [12] [15] [16] |
| Q5 | Real coding-agent prompts grow by patching observed failures; swapping prompts on one model changes workflow while pass rates stayed equal; repository context files do not raise frontier agents' success and add about 20% cost. | Practitioner plus controlled | [42] [43] [44] [45] |
| Q6 Evaluation | Single runs are noisy even at temperature 0 (2.2 to 6.0 points on SWE-bench Verified with 500 tasks); use repeated runs, paired comparisons, power analysis, pass@k and pass^k. | Controlled plus methods | [48] [49] [37] [47] |
| Q6 | With 15 tasks, detecting a one-task (6.7 point) change needs roughly 30 to 60 runs per arm for one model; pooling seven models cuts that to 5 to 9 if the effect is shared. | My arithmetic | section 2, Q6 |
| Q6 | Qualitative review: open-code at least 30 transcripts, cluster failures, count them, stop when new transcripts add no new failure types; freeze the rubric first because reviewers' criteria drift. | Practitioner plus controlled | [50] [51] [47] |

## 2. Key sources by question

### Q1. Does the style of a system prompt carry into outputs and reasoning?

Short answer: probably somewhat for formatting and register, weakly for specific tics, and nobody has tested the exact claim on current frontier agents.

- Anthropic's current prompting guide, which covers Opus 5.5, Opus 5 and Sonnet 5 [1], says the formatting style of the prompt may influence the response and gives the example that "removing markdown from your prompt can reduce the volume of markdown in the output." It offers this as a lever to try when explicit formatting instructions are not enough. The Opus 5 and Sonnet 5 pages [2] [3] add that positive examples of the wanted communication style work better than instructions about what to avoid. Vendor, Claude only.
- Blevins, Schmalwieser and Roth (EACL 2026) [17] had 16 Llama 3 and Gemma 3 models (1B to 70B, base and tuned) continue real dialogues. Models converged to the conversation's style, "often significantly overfitting relative to the human baseline." Tuned and larger models converged less, and the tuned 70B model was close to human levels on length and lexical novelty. Controlled, open-weight, dialogue turns rather than a system prompt.
- Elsweiler, Elsweiler and Ziegner (CHIIR 2026) [20] ran 18,000 simulated conversations on three open-weight models. Engagement-seeking user phrasing produced replies up to 90% longer, with more information but lower density, than hyper-efficient phrasing. Controlled, open-weight, user turns.
- Two results bound the effect. Reinhart et al. (PNAS 2025) [18] found tuned GPT-4o and Llama 3 keep a dense, noun-heavy style that departs from human genre conventions, with larger departures than their base models; the author's notes say this held even for fiction and TV-script prompts. Freeburg (preprint, Mar 2026) [19] counted em dashes across 12 models: a prose-only instruction cut Claude Opus 4.6 to 0.19 per 1,000 words while GPT-4.1 stayed at 9.10, and an explicit ban took Claude Opus to zero while GPT-4.1 kept 3.86 (human average 3.23). Tics look largely trained in, and how well a direct instruction removes them depends on the model family. Neither study varied the prompt's own punctuation or phrasing.
- Reasoning traces. ReasonIF (Kwon et al., Oct 2025) [21] found GPT-OSS, Qwen3 and DeepSeek-R1 followed explicit instructions about their reasoning (language, format, length) under 25% of the time, and less on harder tasks. If explicit instructions barely steer reasoning traces, implicit style cues in a system prompt are unlikely to steer them much. Anthropic does say few-shot examples containing thinking tags shape Claude's own thinking style [1]. I found no measurement of implicit style transfer into reasoning.
- Within a transcript, the agent's own earlier messages become context. Sinha et al. (ICLR 2026) [22] found models make more mistakes when the context holds their own earlier mistakes, and that thinking reduces the effect. Manus [35] reports the same pattern from production ("Language models are excellent mimics") and adds deliberate variation to avoid ruts. This gives the owner's concern a second channel: early sloppy turns can seed later ones.

Net: prompt-to-output transfer of register and formatting is plausible, vendor-endorsed and indirectly supported. For specific tics on 2026 frontier models the size of the effect is unmeasured and is probably smaller than the model's trained house style. Practice 12 in section 3 describes a direct test.

### Q2. How does adherence change with instruction count and prompt length?

- Harada et al., "When Instructions Multiply" (EMNLP 2025 Findings) [23]. Ten LLMs, up to 10 simultaneous instructions for text (ManyIFEval) and 6 for code (StyleMBPP). Success on all instructions fell consistently with count, and a logistic regression on count alone predicted it within about 10%. The earlier "Curse of Instructions" version reported all-ten success of 15% for GPT-4o and 44% for Claude 3.5 Sonnet, rising to 31% and 58% with self-refinement. I took those numbers from a secondary summary because the OpenReview page would not load for me. 2024 models.
- IFScale, Jaroslawicz et al. (Distyl AI, July 2025) [24]. 20 models, up to 500 keyword-inclusion instructions in one writing task. o3 and Gemini 2.5 Pro stayed near perfect to about 150 instructions and then fell; GPT-4.1 and Claude 3.7 Sonnet declined steadily; GPT-4o, Llama 4 Scout and Claude 3.5 Haiku dropped early. The best score at 500 was 68%. Earlier instructions were satisfied more often, most strongly around 150 to 200 instructions, and most errors were omissions. Including a keyword is far easier than following a behavioral rule, so treat 150 as a generous upper bound.
- AgentIF, Qi et al. (NeurIPS 2025 Datasets and Benchmarks) [25]. 707 instructions from 50 real agent applications, averaging 1,723 words and 11.9 constraints. The best model, o1-mini, satisfied 59.8% of constraints; full-instruction success was much lower and near zero above 6,000 words. Condition and tool constraints were hardest. More than 30% of condition-constraint errors were incorrect condition checks, meaning the model did not recognize that the rule applied. About a quarter of instructions had meta-constraints (rules about other rules), and models did worst at choosing between them. Models include GPT-4o, Claude 3.5 Sonnet, DeepSeek-V3 and R1, GLM-Z1-32B and o1-mini. This is the closest benchmark to Evener's situation, on older models.
- Elder, Duesterwald and Muthusamy (Oct 2025) [26] traced part of the decline with count to tension and conflict among instructions and built a conflict score that predicts it.
- Position and time. Li et al. (COLM 2024) [27] measured system-prompt adherence drifting within eight dialogue rounds (LLaMA2-70B-chat, GPT-3.5) and linked it to attention decay; re-inserting the system prompt before user turns reduced drift at a context cost. OpenAI's GPT-4.1 guide [14] reports that instructions placed both before and after long context worked best, and that when instructions conflict the later one tends to win. Li et al., "When Thinking Fails" (May 2025) [28], found chain-of-thought pulls attention away from constraint tokens; the accuracy cost was small for frontier models (Claude 3.7 Sonnet lost 2.8 points on ComplexBench) and large for small ones (Llama-3-8B lost 16.2 on IFEval).
- Vendor statements. Claude Code's docs [5] say that when Claude keeps doing something despite a rule against it, "the file is probably too long and the rule is getting lost," and that emphasizing many lines makes none stand out. OpenAI's GPT-5.6 guidance (July 2026) [11] reports that in a sample of internal coding-agent eval runs, leaner system prompts improved scores by roughly 10 to 15% while cutting total tokens 41 to 66% and cost 33 to 67%. It does not say what was removed.

No study gives a reliable number of behavioral rules a 2026 frontier agent can hold. The direction is consistent: each added rule lowers adherence to the others, conflicting rules lower it further, and many conditional-rule failures are triggers that went unnoticed.

### Q3. Why do stated rules fail at decision time, and what changes behavior?

The pattern is documented.
- Schmied et al. (Google DeepMind, Apr 2025) [29]: Gemma2 27B wrote correct bandit rationales 87% of the time, yet with a correct rationale in hand it still took the greedy action 58% of the time and the optimal one 21%. RL fine-tuning on its own rationales narrowed the gap. Older, smaller model.
- Wang (preprint, May 2026) [30]: Claude Haiku 4.5 and DeepSeek-Reasoner playing poker misapplied rules they had just stated, and 99.5% of Haiku's misapplications leaned toward the cautious option. A trained default overrode a rule the model had computed correctly. Rule misapplication was about a third of Haiku's errors and 8% of DeepSeek's. Single author, narrow domain.

The interviews themselves are weak evidence of mechanism. Chen et al. (Anthropic, May 2025) [31] found reasoning models mention a hint they actually used often less than 20% of the time. An agent quoting a rule after a failure shows it can retrieve the rule. It does not show what drove the decision.

What moved behavior in controlled or measured work:
- Concrete triggers with fixed acts. In Wang [30], restating the rule as a numeric trigger with a required action and no hedging cut misapplication from 13.9% to 6.8%. That prompt also used capitals and MUST, so concreteness and emphasis are confounded.
- Guidance for the current state. AutoGuide (Fu et al., NeurIPS 2024) [32] extracted conditional guidelines from past runs, each tied to the situation where it applies, and retrieved only those matching the agent's current state. It beat ReAct and ExpeL; ExpeL puts a static list of lessons in the prompt. 2024 models.
- Reminders near the decision. Re-inserting the system prompt reduced drift [27]. Anthropic reports that when an Opus 5.5 agent had gone several tool calls without a user-facing update, appending a one-line reminder after the tool results "roughly halved the share of tasks with a long silent stretch," with no measurable cost change [4]. Manus [35] rewrites a todo file at each step so the goal stays in recent context.
- Mechanisms. In SWE-agent (Yang et al., NeurIPS 2024) [34], with GPT-4 Turbo, an edit command whose linter rejects syntactically invalid edits scored 18.0% on SWE-bench Lite against 15.0% without the linter, and window size and search design moved results by similar amounts. Anthropic reports that making a tool require absolute file paths removed a class of path mistakes entirely ("poka-yoke your tools") [6]. Claude Code's docs describe hooks as deterministic and CLAUDE.md rules as advisory [5].
- Examples. Sarukkai, Xie and Fatahalian (May 2025) [33] used an agent's own successful trajectories as in-context examples and raised ALFWorld from 73% to 89% for GPT-4o-mini, a larger gain than switching to GPT-4o. Anthropic recommends a few canonical examples in place of a long list of edge-case rules [7].

Counter-evidence on cutting. ACE (Zhang et al., Oct 2025) [36] found detailed, itemized playbooks of domain strategies beat concise summaries on AppWorld (+10.6%) and warns that prompt optimizers drift toward short, generic prompts that lose domain facts. The helpful content was task-specific operational knowledge such as API quirks and known failure modes. It supports keeping concrete facts; it does not support generic exhortations.

### Q4. Phrasing: negation, emphasis, reasons

- Tone and emphasis on accuracy. Wharton's Prompting Science Reports ran each GPQA question 100 times on GPT-4o and 4o-mini [37], and 25 times on five models including o4-mini [38]. A polite prefix against a commanding one, and threats against tips, made no reliable aggregate difference, while single questions swung both ways. These are knowledge-question accuracy tests, so they do not show whether emphasis changes which action an agent takes.
- Emphasis on behavior. Vendors say it does change behavior on newer models, usually for the worse. Anthropic [1] reports that Opus 4.5 and 4.6 overtrigger on prompts written to fix undertriggering and suggests dropping capitalized CRITICAL and MUST framing from tool-use instructions in favor of plain phrasing. That was measured on 4.5 and 4.6; the 5-series pages say older prompts carry over. OpenAI's GPT-5 guide [9] reports that Cursor's all-caps thoroughness wording made GPT-5 call search repeatedly on small tasks. GPT-5.5 guidance [10] reserves ALWAYS and NEVER for true invariants and asks for decision rules on judgment calls. The GPT-4.1 guide [14] calls capitals and bribes generally unnecessary. Claude Code [5] says to emphasize at most one skipped line. Anthropic's own sample prompts still use NEVER and MUST in places [1], so vendor practice is less strict than vendor advice.
- Negative phrasing. Anthropic [1] [2] [3] recommends saying what to do, and says positive examples beat instructions about what to avoid. Two vendor details complicate this. Opus 5.5 responds well to a list of specific patterns to avoid, while a vague prohibition against a generic look just swaps one default for another [4]. On Opus 5, naming thinking tags in a prohibition worked worse than a general rule, and a rule telling the model not to think increased tag leakage [2]. So naming a specific observable pattern can work, and naming a token to suppress can prime it. Controlled research on negation measures comprehension: larger models handle negation better [39], and small models flip their stance under negated framing far more than commercial ones [40]. I found no controlled test of "don't X" against "do Y" for agent rules on current models. The widely shared "pink elephant" posts are anecdotal.
- Reasons. Anthropic [1] recommends stating the motivation. Its example swaps a bare all-caps ban on ellipses for a sentence explaining that a text-to-speech engine cannot pronounce them, and says Claude generalizes from the explanation. Anthropic's constitution [8] argues narrow, unexplained rules generalize badly, but it is talking about training. I found no controlled prompt-level test.
- Conflicts. OpenAI [9] says contradictory instructions hurt GPT-5 more than earlier models because it "expends reasoning tokens searching for a way to reconcile the contradictions." GPT-5.5 guidance [10] adds that conflicts combined with high reasoning effort produce overthinking. ConInstruct (He et al., AAAI 2026) [41] found strong models detect conflicts well when asked (DeepSeek-R1 F1 91.5%, Claude Sonnet 4.5 87.3%) yet rarely tell the user, and instead quietly satisfy part of the constraints.

### Q5. Vendor guides and published analyses

Where Anthropic [1] to [8] and OpenAI [9] to [14] agree:
- Start small and add rules only for observed failures. OpenAI: "Start with the smallest prompt that preserves the product contract" [10]. GPT-5.6 guidance adds "State each instruction once," expose only relevant tools with short descriptions, and remove one group of instructions, examples or tools at a time while rerunning the same evals [11]. Anthropic: for each line, ask whether removing it would cause mistakes, and convert rules the model already follows into hooks or delete them [5].
- Remove scaffolding written for weaker models. Verification and re-check instructions on Opus 5 [2]. Anthropic says of those instructions that "removing them reduces wasted tokens with no loss in quality." Also thoroughness wording on GPT-5 [9], preamble and upfront-plan prompts on gpt-5.1-codex-max, which could make it stop early, although the newer gpt-5.3-codex starter prompt asks for short preambles again [13], forced progress messages on Sonnet 5 [3], and step-by-step process lists on GPT-5.5, which OpenAI says can narrow the search and make answers mechanical [10].
- Plain, direct language. Emphasis only for invariants. Outcome, success criteria and stopping conditions in place of step lists [10] [11].
- Examples steer format and tone reliably, and should agree with the rules [1] [14]. Keep examples and style guidance when they encode a product requirement [11].
- Treat each technique as measured on a specific model and re-check it on your own evals [1] [10].

Where they differ by family:
- Claude Opus 5 verifies and self-corrects without being asked, narrates heavily, writes long documents and delegates readily [2]. Opus 5.5 finishes with fewer tokens, may end a turn to report progress during unattended runs, and responds to instructions that name the specific early stops to avoid [4]. Sonnet 5 is literal: it "does not silently generalize an instruction from one item to another" [3], so a rule must name its scope, and a qualitative bar such as asking it to be conservative is followed literally.
- GPT-5.5 and 5.6 favor outcome-first prompts with minimal process [10] [11]. GPT-6 Astra tends toward heavily formatted answers and asks clarifying questions more than earlier models [12]. The Codex line is trained on its own harness tools, such as apply_patch, and its own starter prompt [13].
- Z.ai publishes no prompt-style guide for GLM-5.x. Its coding-agent page says context matters more than prompting tricks and that long-lived rules belong in project files [15]. GLM-5.3 forces thinking on.
- DeepSeek publishes no prompt guide for V4 or V4.1; the model cards give sampling settings and three thinking modes [16]. Its only prompt advice, for R1 in January 2025, was to avoid a system prompt and prompt zero-shot. Whether that holds for V4.1 is unknown. DeepSeek documents integrations with Claude Code and Codex, and Z.ai with Claude Code and several other agents, so those harnesses' prompts are a normal operating environment for both.

Published analyses of real agent prompts:
- Breunig and Sriraman (Feb 2026) [42] compared the prompts of Claude Code, Cursor, Gemini CLI, Codex CLI, OpenHands and Kimi CLI. Claude Code's and OpenHands' prompts are under half the length of Codex's and Gemini's. Swapping the Claude Code and Codex prompts on Opus 4.5 changed the workflow at once: Codex's prompt produced understand-then-implement and Claude Code's produced try-and-fix. All combinations passed the test tasks. Small sample.
- Sriraman (Feb 2026) [43] catalogs patches in these prompts that were written after specific failures, including a sycophancy rule that "didn't work well enough," plus contradictions between agents. These prompts grew by reactive patching, the same way Evener's did.
- Gloaguen et al. (ETH Zurich, Feb 2026) [44]: repository context files did not generally raise task success and raised cost by more than 20%. Agents did follow the instructions, and repository overviews did not help. Khatri (July 2026) [45]: on Claude Code and Codex, 17 tasks, 288 runs, context strategy did not measurably change correctness, with effects bounded within 10 to 15 points; failures came from implementation difficulty. Shepard and Albrecht (June 2026) [46]: for a small open model (Qwen3.5-35B-A3B), iteratively tuned guidance beat static guidance (33.0% against 28.3%, and 25.5% with none, four trials). Guidance quality matters more for weaker models.

### Q6. Evaluating prompt changes

- Variance. Bjarnason, Silva and Monperrus (Feb 2026) [49] collected 60,000 SWE-bench Verified trajectories. Single-run pass@1 moved 2.2 to 6.0 points depending on which run was taken, the standard deviation stayed above 1.5 points at temperature 0, and trajectories diverged within the first few percent of tokens. Their advice: multiple runs per task, power analysis, and reporting pass@k and pass^k. Meincke et al. [37] saw per-question answers change across 100 runs at temperature 0.
- Methods. Miller (Anthropic, Nov 2024) [48]: report standard errors, cluster by task, resample each task, compare conditions with paired differences on the same tasks, and size the experiment with a power calculation. Anthropic's agent-eval guide (Jan 2026) [47]: start from 20 to 50 tasks drawn from real failures, run several trials, choose pass@k (one success is enough) or pass^k (every trial must succeed), grade what the agent produced, and read transcripts to check that graders are right.
- Transcript review. Husain and Shankar (June 2025, updated Sept 2026) [50]: annotate at least 30 traces by hand with open notes, cluster the notes into a failure taxonomy, count each failure type, and continue until new traces stop revealing new failure types. Shankar et al. (Apr 2024) [51]: reviewers' criteria shift as they read outputs ("criteria drift"), so settle the rubric on a pilot set and freeze it before comparing conditions.
- DeltaSelect (Conn, Sept 2026) is a recent method for cheap baseline-versus-candidate comparisons of coding agents, including a prompt case study (https://arxiv.org/abs/2609.19607). I did not evaluate it closely.

Worked numbers (my arithmetic, not from a source). Assumptions: paired design with the same 15 tasks under both prompts, pass/fail per run, independent runs, 80% power, two-sided alpha of 0.05. "Flippy" tasks pass half the time; stable tasks never change.

| Setup | Runs per arm to detect 6.7 points (one task) | 10 points | 20 points |
|---|---|---|---|
| One model, all 15 tasks flippy | 59 | 27 | 7 |
| One model, 8 of 15 flippy | 32 | 14 | 4 |
| Seven models pooled, all flippy | 9 | 4 | 1 |
| Seven models pooled, 8 of 15 flippy | 5 | 2 | 1 |

Two identical runs of a task with pass rate p disagree with probability 2p(1-p), which is at most 0.5. Fifteen tasks therefore average at most 7.5 disagreements per rerun. If Evener's "6 to 10 points" are task outcomes, nearly every task is close to a coin flip, and the first row applies. With one run per arm, the 95% interval on a paired difference for one model is roughly plus or minus 26 to 36 points. Pooling assumes the prompt has the same effect on every model, which the vendor guidance says is unlikely, so use pooled numbers only to catch an across-the-board regression.

### How the two internal findings hold up

Rules are inert at decision time. The literature supports this. Models retrieve and state rules they then fail to apply [29] [30]. Over 30% of conditional-rule failures are triggers the model did not notice [25]. Adherence to early instructions decays over long contexts and during reasoning [27] [28]. Anthropic describes the same symptom and blames file length [5]. Of the proposed remedies:
- Mechanisms have the strongest support [34] [6] [5] [4] [27] [32].
- Reconciling colliding rules is well supported [9] [10] [26] [41] [25].
- Operational pre-flight acts have thinner support: one narrow preprint [30], the vendor finding that naming concrete failure patterns works on Opus 5.5 [4], and Sonnet 5's response to concrete bars [3]. Opus 5's over-verification [2] pulls the other way when an act adds a new step.

One caution: the interviews are weak evidence of mechanism [31], so treat them as a source of hypotheses to test.

The noise floor is high and prose rules show little benefit. The variance is in line with the literature [49] [37]. The dilution claim is in line with the density results [23] [24] [25] [26], with OpenAI's measured gains from leaner prompts [11], and with the context-file studies on frontier agents [44] [45]. Specific rules can still have large effects when they change a default policy. GPT-4.1's three agentic reminders (persistence, tool use, planning) "increased our internal SWE-bench Verified score by close to 20%" [14]. One explore-first sentence let Opus 5.5 complete noticeably more multi-app tasks [4]. Tuned guidance helped a small open model [46]. At 15 tasks and one run per arm, a single-model effect smaller than roughly 25 to 35 points cannot be separated from noise (Q6), so the absence of measured benefit is partly a power limit.

## 3. Implications for the rewrite

1. Keep a rule only when removing it would cause a failure you have seen, and state it once. OpenAI [10] [11] and Anthropic [5] both give this test, and OpenAI measured a 10 to 15% score gain from leaner coding-agent prompts [11]. The density research [23] [24] [25] [26] explains the mechanism. Strength: strong vendor agreement plus consistent controlled direction. Weak spot: no study gives a safe rule count for behavioral rules.

2. Delete rules that restate behavior the current models already show, starting with verification, thoroughness, progress-narration and planning scaffolds. For Opus 5, Anthropic says to remove verification instructions outright [2]; OpenAI reports the same pattern for thoroughness wording, preambles and process steps [9] [10] [13]. Conflict: weaker models may still need some of these. GPT-4.1 gained about 20% from three such reminders [14], and a small open model gained from tuned guidance [46]. Run the cuts past the GLM flash and DeepSeek flash models before accepting them.

3. Write each kept rule as a trigger the agent can recognize plus the act to take, placed where the trigger occurs. Missed triggers account for over 30% of conditional-rule errors [25]; a concrete trigger with a fixed act halved rule misapplication [30]; Opus 5.5 responds to named failure patterns [4]; Sonnet 5 applies a rule only to the scope it names [3]. Prefer acts that improve an existing step, such as naming a wrong implementation a self-check would catch before trusting it, over acts that add steps, because added verification steps cause over-verification on Opus 5 [2]. Strength: moderate direction, thin controlled base, one known conflict.

4. Move every rule the harness can enforce into a mechanism, and deliver the rest at the moment they apply. Candidates: tool arguments that make the mistake impossible [6] [34], hooks or gates for git safety and background-job hygiene [5], and short reminders appended after tool results when a known risky state is detected [4] [27]. Anthropic measured a halving of one failure mode from a single appended reminder on Opus 5.5 [4], and state-conditioned guidance beat a static list [32]. On Opus 5.5, append reminders as turn-scoped messages and leave the system prompt unchanged, because editing the system prompt mid-session invalidates earlier thinking blocks [4] [1]. Strength: the best-supported practice in this review.

5. Resolve every pair of rules that can collide, in the text of the rules. Say which one wins and when. Contradictions waste reasoning on GPT-5-class models [9] [10], tension explains part of the density penalty [26], and models settle conflicts silently and unpredictably [41]. A model is good at listing conflicts when asked [41], so run a conflict pass over the draft with a strong model and resolve what it finds. Strength: moderate, consistent.

6. Remove emphasis. Keep capitals or "never" for at most one or two true invariants, such as destructive git operations, and put the reason next to them. Vendors agree that emphatic wording causes overtriggering on newer models [1] [9] [10] [14] and that emphasis stops working when spread across lines [5]. Tone words have no reliable aggregate effect on accuracy [37] [38]. Conflict: the one controlled rule-application result used MUST alongside a concrete trigger [30], so emphasis is confounded there.

7. Phrase rules as what to do. Use "don't" only for a specific, observable pattern, and give the reason in a clause. This rests on vendor guidance [1] [2] [3] and the Opus 5.5 finding that specific avoid-lists work while vague prohibitions swap defaults [4]. Avoid naming a token you want suppressed, which backfired on Opus 5 [2]. Keep negations simple for the flash models, since negation handling is weaker in smaller models [39] [40]. Strength: weak to mixed; no controlled test on agent rules.

8. Replace clusters of rules with two or three short examples of the target behavior and voice, and vary them. Examples are the most reliable style lever Anthropic names [1] [2] [3], curated examples beat long edge-case lists [7], and trajectory examples beat a model upgrade in one study [33]. Examples are copied closely [35] and must agree with the rules [14], and a literal model such as Sonnet 5 may copy surface details [3]. Strength: moderate.

9. Keep concrete operational facts even when they are long, and cut generic value statements. Detailed domain playbooks helped [36]; repository overviews and self-evident practices did not [44] [5]; Z.ai says context matters more than prompting tricks [15]. This reconciles the pull toward brevity with the pull toward detail: shorten exhortation and keep facts the model cannot infer. Strength: moderate.

10. Move machinery explanations into tool descriptions and keep those short and exact. Anthropic spent more effort on tools than on the prompt for its SWE-bench agent [6] and names bloated tool sets as a common failure [7]. OpenAI says to expose only relevant tools with concise descriptions [11]. AgentIF found tool constraints among the hardest to follow when they sit in long instructions [25]. Strength: moderate vendor plus one controlled data point.

11. Write the prompt in the voice you want back: plain paragraphs, no em dashes, no bold-label lists, no arrows, no capitals. Add one short positive description of the wanted writing style plus an example. Vendor support is direct for Claude [1] and controlled support is indirect [17] [20]. Expect a modest effect on tics: trained house style is strong [18], and a direct instruction removed em dashes on Claude Opus 4.6 but only partly on GPT-4.1 [19]. If a tic matters, name it once with the alternative and measure it per model. Strength: weak for tics, moderate for formatting and register.

12. Evaluate with paired, repeated runs and blind transcript review, and measure writing mechanically. Concretely:
    - Run old and new prompts on the same tasks, several runs each. Use the Q6 table to pick run counts, and report pass^k for reliability [47] [48] [49].
    - Count tics per 1,000 words in agent messages and, where available, reasoning summaries, per model and prompt: em dashes, "not X but Y" and "X, not Y" patterns, bold-label bullets, arrows, all-caps words. This directly tests the owner's hypothesis, which no published study has done.
    - For behavior, build the failure taxonomy from past transcripts first [50], freeze the rubric [51], hide from the reviewer which prompt produced each transcript, and do not use the agent's own account of why it acted [31].
    Strength: strong for the statistics, practitioner-level for the review process.

13. Tune and check per model family, with the flash models as the gate. Both main vendors treat guidance as model-specific [1] [10]. Smaller and open models are more sensitive to instruction density [24], reasoning-induced constraint loss [28], negation [40] and guidance quality [46], while frontier agents barely respond to context files [45]. For DeepSeek V4.1 flash, test whether moving the rules into the first user turn changes behavior, since R1-era advice put instructions there [16]. Strength: moderate direction, little data on GLM-5.3 or DeepSeek V4.1 specifically.

## 4. Open questions the literature does not settle

1. Does a system prompt's own style (em dashes, "X, not Y", bold labels, aphorisms) raise the rate of those tics in a frontier agent's output? Nothing I found varies the prompt's style and measures output tics on current models. The closest work uses user turns on open models [17] [20] or explicit bans [19].
2. Does prompt style reach hidden reasoning? Explicit reasoning instructions are followed poorly by open reasoning models [21]; Anthropic says examples shape thinking style [1]. Implicit transfer is unmeasured.
3. How many behavioral rules can a 2026 frontier agent hold? The density benchmarks use checkable formatting or keyword constraints [23] [24] or 2024-25 models [25].
4. Do operational pre-flight acts beat value statements on current frontier agents? The controlled evidence is one narrow preprint with a confound [30], and Opus 5's over-verification [2] suggests some acts add cost without benefit.
5. Does "don't X" underperform "do Y" for agent rules on current models? Vendor guidance is mixed [2] [4] and the controlled work is about comprehension [39] [40].
6. Does stating the reason behind a rule change behavior at inference time? Only vendor claims exist [1] [8].
7. How do GLM-5.3 and DeepSeek V4.1 respond to prompt style, emphasis and placement? Neither vendor publishes guidance [15] [16]. Both vendors document running their models inside Claude Code and other agent harnesses, which suggests those prompt styles are familiar to them, but I found no data.
8. Are post-hoc agent interviews valid diagnostics? Chain-of-thought faithfulness work [31] says self-reports miss real causes; no study tests interviews of coding agents specifically.
9. How do prompt changes interact with reasoning effort? OpenAI says conflicts plus high effort cause overthinking [10], and Anthropic says effort changes thinking more reliably than prompt text [4]. A prompt comparison needs effort held fixed per model, and results may not transfer across effort levels.

## References

Vendor guidance
- [1] Anthropic. "Prompting best practices." Claude Platform Docs (covers Opus 5.5, Opus 5, Sonnet 5 and others). Accessed 2026-09-26. https://platform.claude.com/docs/en/build-with-claude/prompt-engineering/claude-prompting-best-practices
- [2] Anthropic. "Prompting Claude Opus 5." Accessed 2026-09-26. https://platform.claude.com/docs/en/build-with-claude/prompt-engineering/prompting-claude-opus-5
- [3] Anthropic. "Prompting Claude Sonnet 5." Accessed 2026-09-26. https://platform.claude.com/docs/en/build-with-claude/prompt-engineering/prompting-claude-sonnet-5
- [4] Anthropic. "Prompting Claude Opus 5.5." Accessed 2026-09-26. https://platform.claude.com/docs/en/build-with-claude/prompt-engineering/prompting-claude-opus-5-5
- [5] Anthropic. "Best practices for Claude Code." Claude Code Docs. Accessed 2026-09-26. https://code.claude.com/docs/en/best-practices
- [6] Anthropic. "Building effective agents." 2024-12-19. https://www.anthropic.com/engineering/building-effective-agents
- [7] Anthropic. "Effective context engineering for AI agents." 2025-09-29. https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents
- [8] Anthropic. "Claude's Constitution." January 2026. https://www.anthropic.com/constitution
- [9] OpenAI. "GPT-5 prompting guide." OpenAI Cookbook, August 2025 (page undated). https://developers.openai.com/cookbook/examples/gpt-5/gpt-5_prompting_guide
- [10] OpenAI. "Prompt guidance: GPT-5.5." April 2026 (date from S. Willison's post of 2026-04-25). https://developers.openai.com/api/docs/guides/prompt-guidance?model=gpt-5.5
- [11] OpenAI. "Prompt guidance: GPT-5.6." July 2026 (press coverage dated 2026-07-13). https://developers.openai.com/api/docs/guides/prompt-guidance?model=gpt-5.6
- [12] OpenAI. "Model guidance" (GPT-6 Astra, Sol, Luna). Undated, accessed 2026-09-26. https://developers.openai.com/api/docs/guides/latest-model
- [13] OpenAI. "Codex Prompting Guide" (gpt-5.3-codex; earlier gpt-5.1-codex-max). OpenAI Cookbook, undated. https://developers.openai.com/cookbook/examples/gpt-5/codex_prompting_guide. The earlier GPT-5-Codex guide's "less is more" advice and the claim that the Codex CLI prompt used about 40% of GPT-5's tokens come from secondary sources; the original page now returns 404.
- [14] OpenAI. "GPT-4.1 Prompting Guide." OpenAI Cookbook, April 2025. https://developers.openai.com/cookbook/examples/gpt4-1_prompting_guide
- [15] Z.ai. "Best Practices for Coding Agents" (DevPack) and "Migrate to GLM-5.3." Accessed 2026-09-26. https://docs.z.ai/devpack/resources/best-practice.md and https://docs.z.ai/guides/overview/migrate-to-glm-new.md
- [16] DeepSeek. DeepSeek-V4-Flash model card, accessed 2026-09-26, https://huggingface.co/deepseek-ai/DeepSeek-V4-Flash ; DeepSeek-R1 model card, January 2025, https://huggingface.co/deepseek-ai/DeepSeek-R1

Style transfer
- [17] T. Blevins, S. Schmalwieser, B. Roth. "Do language models accommodate their users? A study of linguistic convergence." EACL 2026 (arXiv August 2025). https://arxiv.org/abs/2508.03276
- [18] A. Reinhart et al. "Do LLMs write like humans? Variation in grammatical and rhetorical styles." PNAS 2025 (arXiv October 2024). https://arxiv.org/abs/2410.16107 ; author's notes: https://www.refsmmat.com/notebooks/llm-style.html
- [19] E. M. Freeburg. "The Last Fingerprint: How Markdown Training Shapes LLM Prose." arXiv preprint, March 2026. https://arxiv.org/abs/2603.27006
- [20] D. Elsweiler, C. Elsweiler, A. Ziegner. "Cooking Up Politeness in Human-AI Information Seeking Dialogue." CHIIR 2026 (arXiv January 2026). https://arxiv.org/abs/2601.09898
- [21] Y. Kwon, S. Zhu, F. Bianchi, K. Zhou, J. Zou. "ReasonIF: Large Reasoning Models Fail to Follow Instructions During Reasoning." arXiv, October 2025. https://arxiv.org/abs/2510.15211
- [22] A. Sinha, A. Arun, S. Goel, S. Staab, J. Geiping. "The Illusion of Diminishing Returns: Measuring Long Horizon Execution in LLMs." ICLR 2026 (arXiv September 2025). https://arxiv.org/abs/2509.09677

Instruction density
- [23] K. Harada et al. "When Instructions Multiply: Measuring and Estimating LLM Capabilities of Multiple Instructions Following." EMNLP 2025 Findings. https://aclanthology.org/2025.findings-emnlp.896/ ; earlier "Curse of Instructions" numbers via https://maxpool.dev/research-papers/curse_of_instructions_report.html
- [24] D. Jaroslawicz, B. Whiting, P. Shah, K. Maamari (Distyl AI). "How Many Instructions Can LLMs Follow at Once?" arXiv, July 2025. https://arxiv.org/abs/2507.11538
- [25] Y. Qi et al. "AGENTIF: Benchmarking Instruction Following of Large Language Models in Agentic Scenarios." NeurIPS 2025 Datasets and Benchmarks (arXiv May 2025). https://arxiv.org/abs/2505.16944
- [26] B. Elder, E. Duesterwald, V. Muthusamy. "Boosting Instruction Following at Scale." arXiv, October 2025. https://arxiv.org/abs/2510.14842
- [27] K. Li et al. "Measuring and Controlling Instruction (In)Stability in Language Model Dialogs." COLM 2024 (arXiv February 2024). https://arxiv.org/abs/2402.10962
- [28] X. Li et al. "When Thinking Fails: The Pitfalls of Reasoning for Instruction-Following in LLMs." arXiv, May 2025. https://arxiv.org/abs/2505.11423

Stated vs applied
- [29] T. Schmied, J. Bornschein, J. Grau-Moya, M. Wulfmeier, R. Pascanu (Google DeepMind). "LLMs are Greedy Agents: Effects of RL Fine-tuning on Decision-Making Abilities." arXiv, April 2025. https://arxiv.org/abs/2504.16078
- [30] Y. Wang. "Doing What They Say, Not What They Reason: Locating the Faithfulness Gap in LLM Agents." arXiv preprint, May 2026. https://arxiv.org/abs/2606.00476
- [31] Y. Chen et al. (Anthropic). "Reasoning Models Don't Always Say What They Think." arXiv, May 2025. https://arxiv.org/abs/2505.05410
- [32] Y. Fu et al. "AutoGuide: Automated Generation and Selection of Context-Aware Guidelines for Large Language Model Agents." NeurIPS 2024. https://arxiv.org/abs/2403.08978
- [33] V. Sarukkai, Z. Xie, K. Fatahalian. "Self-Generated In-Context Examples Improve LLM Agents for Sequential Decision-Making Tasks." arXiv, May 2025. https://arxiv.org/abs/2505.00234
- [34] J. Yang et al. "SWE-agent: Agent-Computer Interfaces Enable Automated Software Engineering." NeurIPS 2024 (arXiv May 2024). https://arxiv.org/abs/2405.15793
- [35] Y. Ji (Manus). "Context Engineering for AI Agents: Lessons from Building Manus." 2025-07-18. https://manus.im/blog/Context-Engineering-for-AI-Agents-Lessons-from-Building-Manus
- [36] Q. Zhang et al. "Agentic Context Engineering: Evolving Contexts for Self-Improving Language Models." arXiv, October 2025. https://arxiv.org/abs/2510.04618

Phrasing
- [37] L. Meincke, E. Mollick, L. Mollick, D. Shapiro (Wharton Generative AI Labs). "Prompting Science Report 1: Prompt Engineering is Complicated and Contingent." March 2025. https://arxiv.org/abs/2503.04818
- [38] L. Meincke, E. Mollick, L. Mollick, D. Shapiro. "Prompting Science Report 3: I'll pay you or I'll kill you, but will you care?" August 2025. https://arxiv.org/abs/2508.00614
- [39] T. Vrabcová et al. "Negation: A Pink Elephant in the Large Language Models' Room?" arXiv, March 2025. https://arxiv.org/abs/2503.22395
- [40] K. Elkins, J. Chun. "When Prohibitions Become Permissions: Auditing Negation Sensitivity in Language Models" (retitled in v2). arXiv, January 2026. https://arxiv.org/abs/2601.21433
- [41] X. He et al. "ConInstruct: Evaluating Large Language Models on Conflict Detection and Resolution in Instructions." AAAI 2026 (arXiv November 2025). https://arxiv.org/abs/2511.14342

Analyses of real agent prompts and context files
- [42] D. Breunig, S. Sriraman. "How System Prompts Define Agent Behavior." 2026-02-10. https://www.dbreunig.com/2026/02/10/system-prompts-define-the-agent-as-much-as-the-model.html
- [43] S. Sriraman. "Weird system prompt artefacts." nilenso blog, 2026-02-12. https://blog.nilenso.com/blog/2026/02/12/weird-system-prompt-artefacts/
- [44] T. Gloaguen, N. Mündler, M. Müller, V. Raychev, M. Vechev (ETH Zurich). "Evaluating AGENTS.md: Are Repository-Level Context Files Helpful for Coding Agents?" arXiv, February 2026. https://arxiv.org/abs/2602.11988
- [45] P. Khatri. "Do Context Files Help Coding Agents? A Two-Agent Ablation Study on Real Repositories." arXiv, July 2026. https://arxiv.org/abs/2607.27250
- [46] A. Shepard, J. Albrecht. "Probe-and-Refine Tuning of Repository Guidance for Coding Agents." arXiv, June 2026. https://arxiv.org/abs/2606.20512

Evaluation
- [47] Anthropic. "Demystifying evals for AI agents." 2026-01-09. https://www.anthropic.com/engineering/demystifying-evals-for-ai-agents
- [48] E. Miller (Anthropic). "Adding Error Bars to Evals: A Statistical Approach to Language Model Evaluations." November 2024. https://arxiv.org/abs/2411.00640 ; summary: https://www.anthropic.com/research/statistical-approach-to-model-evals
- [49] B. H. Bjarnason, A. Silva, M. Monperrus. "On Randomness in Agentic Evals." arXiv, February 2026. https://arxiv.org/abs/2602.07150
- [50] H. Husain, S. Shankar. "Why is error analysis so important in LLM evals, and how is it performed?" 2025-06-27, updated 2026-09-01. https://hamel.dev/blog/posts/evals-faq/why-is-error-analysis-so-important-in-llm-evals-and-how-is-it-performed.html
- [51] S. Shankar, J.D. Zamfirescu-Pereira, B. Hartmann, A. Parameswaran, I. Arawjo. "Who Validates the Validators? Aligning LLM-Assisted Evaluation of LLM Outputs with Human Preferences." arXiv, April 2024. https://arxiv.org/abs/2404.12272
