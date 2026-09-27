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
	"unicode/utf8"

	"primeradiant.com/evener/agent/doctor"
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
// returns the key. maskRoot is the directory the labeled results live under.
// Every packet gets the same modification time, so listing the packets by
// time does not group them by version.
func writeReviewPack(dirs []labeledDir, packetsDir, maskRoot string, rng *rand.Rand) ([]reviewEntry, error) {
	for _, d := range dirs {
		if !strings.ContainsAny(d.Label, "0123456789") {
			return nil, fmt.Errorf("label %q needs a digit, such as v0 or v1-A: the label is masked wherever it appears, and an ordinary word would be masked in the agents' own writing", d.Label)
		}
	}
	if err := os.MkdirAll(packetsDir, 0o755); err != nil {
		return nil, err
	}
	written := time.Now()
	used := map[string]bool{}
	var key []reviewEntry
	for _, d := range dirs {
		results, err := loadResults(d.Dir)
		if err != nil {
			return nil, err
		}
		for _, lr := range results {
			res := lr.Result
			rootID, err := rootSessionID(res.StateDir)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", lr.Path, err)
			}
			tr, err := runnerReadTranscript(res.StateDir, rootID, doctor.TranscriptOpts{TextMax: doctor.TextMaxFull})
			if err != nil {
				return nil, fmt.Errorf("%s: %w", lr.Path, err)
			}
			body := maskRunDetails(renderPacket(tr), maskRoot, d.Label)
			name := uniquePacketName(rng, used)
			path := filepath.Join(packetsDir, name)
			content := fmt.Sprintf("# Task %s\n\n%s", res.Probe, body)
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				return nil, err
			}
			if err := os.Chtimes(path, written, written); err != nil {
				return nil, err
			}
			key = append(key, reviewEntry{Packet: name, Label: d.Label, Model: res.Model, Probe: res.Probe, Repetition: res.Repetition, Result: lr.Path})
		}
	}
	slices.SortFunc(key, func(a, b reviewEntry) int { return strings.Compare(a.Packet, b.Packet) })
	return key, nil
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

// packetToolResultMax caps each tool result in a packet. The reader judges
// the agent's work and writing, and a whole file or test log adds little.
const packetToolResultMax = 1500

// renderPacket renders a root transcript for a blind read. Every message to
// the user appears whole, since those messages are what the reader scores.
// Other tool calls appear as their previews, and tool results are cut short.
func renderPacket(tr doctor.TranscriptResult) string {
	var b strings.Builder
	for _, turn := range tr.Turns {
		section, ok := packetSections[turn.Kind]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "## %s\n\n", section)
		if turn.Text != "" {
			fmt.Fprintf(&b, "%s\n\n", turn.Text)
		}
		for _, call := range turn.ToolCalls {
			if !isMessageToUser(call) {
				fmt.Fprintf(&b, "→ %s `%s`\n\n", call.Name, call.ArgPreview)
				continue
			}
			for _, msg := range resultMessages(call.Arguments) {
				fmt.Fprintf(&b, "⇒ %s\n\n%s\n\n", call.Name, msg)
			}
		}
		for _, result := range turn.ToolResults {
			status := ""
			if result.IsError {
				status = " (error)"
			}
			fmt.Fprintf(&b, "← %s%s\n\n%s\n\n", result.Name, status, indentBlock(firstBytes(result.ContentPreview, packetToolResultMax)))
		}
	}
	return b.String()
}

// firstBytes keeps the start of s, at most n bytes, without splitting a rune.
func firstBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
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
	key, err := writeReviewPack(dirs, *packets, *maskRoot, rand.New(rand.NewPCG(*seed, *seed)))
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(key, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(*keyPath, append(data, '\n'), 0o644)
}
