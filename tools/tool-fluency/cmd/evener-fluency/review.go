package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"primeradiant.com/evener/agent/doctor"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmdutil"
)

// reviewEntry maps one blind packet back to its run. The key lives outside
// the packets directory, so a reviewer never sees it.
type reviewEntry struct {
	Packet     string `json:"packet"`
	Label      string `json:"label"`
	Model      string `json:"model"`
	Probe      string `json:"probe"`
	Repetition int    `json:"repetition"`
	Result     string `json:"result"`
}

// writeReviewPack renders each run's root transcript into packetsDir under a
// random name, masks everything that would reveal its prompt version, and
// returns the key plus how many results it skipped. maskRoot is the
// directory the labeled results live under. Every packet gets the same
// modification time, so listing the packets by time does not group them by
// version.
//
// A result whose status is not "passed" or "failed" (skipped_unavailable,
// blocked_harness, blocked_infra) may have no state dir at all — the harness
// never got far enough to create one — so it is skipped rather than packed;
// prose-stats tolerates the same runs (summarizeProse). Packing it anyway
// would abort the whole pack on rootSessionID's "not found" error.
func writeReviewPack(dirs []labeledDir, packetsDir, maskRoot string, rng *rand.Rand) ([]reviewEntry, int, error) {
	for _, d := range dirs {
		if !strings.ContainsAny(d.Label, "0123456789") || !strings.ContainsFunc(d.Label, unicode.IsLetter) {
			return nil, 0, fmt.Errorf("label %q needs a letter and a digit, such as v0 or v1-A: the label is masked wherever it appears, and an ordinary word or number would be masked in the agents' own writing", d.Label)
		}
	}
	if err := os.MkdirAll(packetsDir, 0o755); err != nil {
		return nil, 0, err
	}
	written := time.Now()
	used := map[string]bool{}
	var key []reviewEntry
	skipped := 0
	for _, d := range dirs {
		results, err := loadResults(d.Dir)
		if err != nil {
			return nil, 0, err
		}
		for _, lr := range results {
			res := lr.Result
			if res.Status != "passed" && res.Status != "failed" {
				skipped++
				continue
			}
			rootID, err := rootSessionID(res.StateDir)
			if err != nil {
				return nil, 0, fmt.Errorf("%s: %w", lr.Path, err)
			}
			tr, err := runnerReadTranscript(res.StateDir, rootID, doctor.TranscriptOpts{TextMax: doctor.TextMaxFull})
			if err != nil {
				return nil, 0, fmt.Errorf("%s: %w", lr.Path, err)
			}
			body := maskRunDetails(renderPacket(tr), maskRoot, d.Label)
			name := uniquePacketName(rng, used)
			path := filepath.Join(packetsDir, name)
			content := fmt.Sprintf("# Task %s\n\n%s", res.Probe, body)
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				return nil, 0, err
			}
			if err := os.Chtimes(path, written, written); err != nil {
				return nil, 0, err
			}
			key = append(key, reviewEntry{Packet: name, Label: d.Label, Model: res.Model, Probe: res.Probe, Repetition: res.Repetition, Result: lr.Path})
		}
	}
	slices.SortFunc(key, func(a, b reviewEntry) int { return strings.Compare(a.Packet, b.Packet) })
	return key, skipped, nil
}

// packetSections name the transcript turns a blind reader sees: what the user
// asked, what the agent said and did, and what its tools returned. Every other
// turn is harness chrome, and some of it, such as the environment turn's date,
// would tell the reader which runs go together.
var packetSections = map[string]string{
	string(schema.TurnUserInput):   "User",
	string(schema.TurnSteering):    "User",
	string(schema.TurnAssistant):   "Agent",
	string(schema.TurnTool):        "Tool results",
	string(schema.TurnToolResults): "Tool results",
}

// harnessSteeringSection labels steering the person never saw, such as the
// bare-text nudge the harness injects after a turn without a message to the
// user. Rendering it under "User" would tell the blind reader the person said
// it; a steering turn is the person's own words only when its source is
// SteeringSourceUser.
const harnessSteeringSection = "Evener (not shown to the user)"

// packetToolResultMax caps each tool result in a packet. The reader judges
// the agent's work and writing, and a whole file or test log adds little.
const packetToolResultMax = 1500

// renderPacket renders a root transcript for a blind read. Every message to
// the user appears whole, since those messages are what the reader scores.
// Other tool calls appear as their previews, except delegate and
// delegate_send: their brief (delegate's prompt and task_list step prompts,
// delegate_send's message) is rendered in full, since a blind reader judging
// how work was split and briefed must see the brief itself, not an 80-byte
// preview. Tool results are cut short.
// A communicate/result-tool message that echoes assistant text already shown
// within the same logical turn is not repeated, matching evener itself: the
// reader must never see a repetition the user never saw (see
// echo_suppression.go). It still gets a line saying the text above was
// delivered: an empty turn where that call was reads, to a blind reader, as
// a report that was never sent, when the app in fact showed it once.
func renderPacket(tr doctor.TranscriptResult) string {
	var b strings.Builder
	var echoes echoSuppressor
	for _, turn := range tr.Turns {
		turnSeq := echoes.observe(turn.Kind, turn.Text)
		section, ok := packetSections[turn.Kind]
		if !ok {
			continue
		}
		if turn.Kind == string(schema.TurnSteering) && turn.SteeringSource != events.SteeringSourceUser {
			section = harnessSteeringSection
		}
		fmt.Fprintf(&b, "## %s\n\n", section)
		if turn.Text != "" {
			fmt.Fprintf(&b, "%s\n\n", turn.Text)
		}
		for _, call := range turn.ToolCalls {
			if !isMessageToUser(call) {
				if brief := delegateBrief(call); brief != "" {
					fmt.Fprintf(&b, "→ %s\n\n%s\n\n", delegateCallHeader(call), brief)
				} else {
					fmt.Fprintf(&b, "→ %s `%s`\n\n", call.Name, call.ArgPreview)
				}
				continue
			}
			if msg := shownMessage(call.Arguments); msg != "" {
				if echoes.echoes(turnSeq, msg) {
					fmt.Fprintf(&b, "⇒ %s (sent the text above to the user)\n\n", call.Name)
				} else {
					fmt.Fprintf(&b, "⇒ %s\n\n%s\n\n", call.Name, msg)
				}
			}
		}
		for _, result := range turn.ToolResults {
			status := ""
			if result.IsError {
				status = " (error)"
			}
			fmt.Fprintf(&b, "← %s%s\n\n%s\n\n", result.Name, status, indentBlock(doctor.Truncate(result.ContentPreview, packetToolResultMax)))
		}
	}
	return b.String()
}

// delegateArguments decodes a delegate call's arguments: the assignment
// prompt, and each task_list step's own prompt when the call seeds a
// multi-step plan instead of a single prompt (agent/internal/tool/
// definitions.go's DefDelegate).
type delegateArguments struct {
	Prompt   string `json:"prompt"`
	TaskList []struct {
		Title  string `json:"title"`
		Prompt string `json:"prompt"`
	} `json:"task_list"`
}

// delegateSendArguments decodes a delegate_send call's arguments: to, the
// delegate_id or "caller" it addressed, and message, the text delivered
// there (DefDelegateSend, agent/internal/tool/definitions.go).
type delegateSendArguments struct {
	To      string `json:"to"`
	Message string `json:"message"`
}

// decodeDelegateSendArguments decodes a delegate_send call's arguments once,
// so delegateBrief and delegateCallHeader never disagree about what a call
// said or where it went.
func decodeDelegateSendArguments(call doctor.ToolCallSummary) (delegateSendArguments, error) {
	var args delegateSendArguments
	err := json.Unmarshal([]byte(call.Arguments), &args)
	return args, err
}

// delegateBrief returns the full text of a delegate or delegate_send call's
// brief: what a blind reader needs to judge how work was split and briefed,
// which the 80-byte argument preview cuts short. It returns "" for any other
// tool, and for a delegate/delegate_send call whose arguments do not decode
// as JSON or carry no prompt/message — renderPacket falls back to the
// preview line then.
func delegateBrief(call doctor.ToolCallSummary) string {
	switch call.Name {
	case "delegate":
		var args delegateArguments
		if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
			return ""
		}
		var b strings.Builder
		b.WriteString(args.Prompt)
		for i, step := range args.TaskList {
			fmt.Fprintf(&b, "\n\nStep %d: %s\n%s", i+1, step.Title, step.Prompt)
		}
		return b.String()
	case "delegate_send":
		args, err := decodeDelegateSendArguments(call)
		if err != nil {
			return ""
		}
		return args.Message
	default:
		return ""
	}
}

// delegateCallHeader returns the "→ ..." label for a delegate or
// delegate_send call whose brief is being shown in full: the call's name, or
// "delegate_send to <to>" naming the delegate or caller a follow-up went to
// — a blind reader judging delegation needs to see who a message was sent
// to, alongside its full text.
func delegateCallHeader(call doctor.ToolCallSummary) string {
	if call.Name != "delegate_send" {
		return call.Name
	}
	args, err := decodeDelegateSendArguments(call)
	if err != nil || args.To == "" {
		return call.Name
	}
	return call.Name + " to " + args.To
}

// indentBlock indents every line of s by four spaces, a Markdown code block
// that holds any tool output, backticks included.
func indentBlock(s string) string {
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
}

// maskRunDetails removes what would reveal a packet's prompt version. Every
// path under maskRoot becomes <run>: run paths carry the version label, and a
// truncated argument preview can cut a path anywhere, so the whole path goes.
// The root's absolute and resolved forms are both masked, for tools that print
// either. The label itself is masked last, as a whole word; writeReviewPack
// refuses labels without a digit, so masking cannot rewrite an ordinary word.
func maskRunDetails(text, maskRoot, label string) string {
	if abs, err := filepath.Abs(maskRoot); err == nil {
		maskRoot = abs
	}
	roots := []string{maskRoot}
	if resolved, err := filepath.EvalSymlinks(maskRoot); err == nil && resolved != maskRoot {
		roots = append(roots, resolved)
	}
	for _, root := range roots {
		re := regexp.MustCompile(regexp.QuoteMeta(root) + "[^\\s\"'`)\\]]*")
		text = re.ReplaceAllString(text, "<run>")
	}
	return regexp.MustCompile(`\b`+regexp.QuoteMeta(label)+`\b`).ReplaceAllString(text, "<version>")
}

func uniquePacketName(rng *rand.Rand, used map[string]bool) string {
	for {
		name := fmt.Sprintf("%08x.md", rng.Uint32())
		if !used[name] {
			used[name] = true
			return name
		}
	}
}

// pathWithin reports whether path lies inside dir.
func pathWithin(dir, path string) (bool, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false, err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil {
		return false, err
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)), nil
}

func runReviewPack(args []string) error {
	fs := flag.NewFlagSet("review-pack", flag.ContinueOnError)
	var results cmdutil.StringSliceFlag
	fs.Var(&results, "results", "LABEL=DIR of a run or matrix output (repeatable)")
	packets := fs.String("packets", "", "directory for the blind packets")
	keyPath := fs.String("key", "", "file for the packet key; must be outside --packets")
	maskRoot := fs.String("mask-root", "", "directory every --results DIR lives under; paths below it are masked")
	seed := fs.Uint64("seed", uint64(time.Now().UnixNano()), "shuffle seed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *packets == "" || *keyPath == "" || *maskRoot == "" {
		return errors.New("--packets, --key, and --mask-root are required")
	}
	if inside, err := pathWithin(*packets, *keyPath); err != nil {
		return err
	} else if inside {
		return errors.New("--key must be outside --packets, or reviewers would see it")
	}
	dirs, err := parseLabeledDirs(results)
	if err != nil {
		return err
	}
	for _, d := range dirs {
		if inside, err := pathWithin(*maskRoot, d.Dir); err != nil {
			return err
		} else if !inside {
			return fmt.Errorf("%s is outside --mask-root %s, so its paths would not be masked", d.Dir, *maskRoot)
		}
	}
	if holdsResults(*packets) {
		return fmt.Errorf("%s already holds packets; name a new --packets directory", *packets)
	}
	if _, err := os.Stat(*keyPath); err == nil {
		return fmt.Errorf("%s already exists; name a new --key file", *keyPath)
	}
	key, skipped, err := writeReviewPack(dirs, *packets, *maskRoot, rand.New(rand.NewPCG(*seed, *seed)))
	if err != nil {
		return err
	}
	if skipped > 0 {
		fmt.Fprintf(os.Stderr, "review-pack: skipped %d run(s) that did not pass or fail (no transcript to pack)\n", skipped)
	}
	data, err := json.MarshalIndent(key, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(*keyPath, append(data, '\n'), 0o644)
}
