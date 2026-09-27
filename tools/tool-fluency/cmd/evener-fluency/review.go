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

	"primeradiant.com/evener/agent/doctor"
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
func writeReviewPack(dirs []labeledDir, packetsDir, maskRoot string, rng *rand.Rand) ([]reviewEntry, error) {
	if err := os.MkdirAll(packetsDir, 0o755); err != nil {
		return nil, err
	}
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
			tr, err := runnerReadTranscript(res.StateDir, rootID, doctor.TranscriptOpts{Format: "markdown", TextMax: doctor.TextMaxFull})
			if err != nil {
				return nil, fmt.Errorf("%s: %w", lr.Path, err)
			}
			body := maskRunDetails(doctor.RenderTranscript(tr, "markdown"), maskRoot, d.Label)
			name := uniquePacketName(rng, used)
			content := fmt.Sprintf("# Task %s\n\n%s\n", res.Probe, body)
			if err := os.WriteFile(filepath.Join(packetsDir, name), []byte(content), 0o644); err != nil {
				return nil, err
			}
			key = append(key, reviewEntry{Packet: name, Label: d.Label, Model: res.Model, Probe: res.Probe, Repetition: res.Repetition, Result: lr.Path})
		}
	}
	slices.SortFunc(key, func(a, b reviewEntry) int { return strings.Compare(a.Packet, b.Packet) })
	return key, nil
}

// maskRunDetails removes what would reveal a packet's prompt version. Every
// path under maskRoot becomes <run>: run paths carry the version label, and a
// truncated argument preview can cut a path anywhere, so the whole path goes.
// The root's resolved form is masked too, for tools that print resolved
// paths. The label itself is masked last; pick labels that do not occur in
// ordinary prose, such as v1-A, so masking cannot change what the agent wrote.
func maskRunDetails(text, maskRoot, label string) string {
	roots := []string{filepath.Clean(maskRoot)}
	if resolved, err := filepath.EvalSymlinks(maskRoot); err == nil && resolved != roots[0] {
		roots = append(roots, resolved)
	}
	for _, root := range roots {
		re := regexp.MustCompile(regexp.QuoteMeta(root) + "[^\\s\"'`)\\]]*")
		text = re.ReplaceAllString(text, "<run>")
	}
	return strings.ReplaceAll(text, label, "<version>")
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
