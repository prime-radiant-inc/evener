package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"primeradiant.com/evener/agent/doctor"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/internal/apptranscript"
)

// runProse is the agent prose one run produced. ToUser holds what the root
// session sent the user through its result tool. All adds every assistant
// text turn and every result-tool message from every session, delegates
// included.
type runProse struct {
	ToUser []string
	All    []string
}

// extractRunProse reads the prose out of every transcript in a run's state
// directory.
func extractRunProse(stateDir string) (runProse, error) {
	rootID, err := rootSessionID(stateDir)
	if err != nil {
		return runProse{}, err
	}
	var p runProse
	err = walkTranscripts(stateDir, doctor.TranscriptOpts{TextMax: doctor.TextMaxFull}, func(tr doctor.TranscriptResult) error {
		for _, turn := range tr.Turns {
			if turn.Kind != string(schema.TurnAssistant) {
				continue
			}
			if text := strings.TrimSpace(turn.Text); text != "" {
				p.All = append(p.All, text)
			}
			for _, call := range turn.ToolCalls {
				if !isMessageToUser(call) {
					continue
				}
				p.All = append(p.All, resultMessages(call.Arguments)...)
				if msg := shownMessage(call.Arguments); msg != "" && tr.SessionID == rootID {
					p.ToUser = append(p.ToUser, msg)
				}
			}
		}
		return nil
	})
	return p, err
}

// isMessageToUser reports whether a tool call carries a message to the user:
// a call to the session's result tool, or to communicate.
func isMessageToUser(call doctor.ToolCallSummary) bool {
	return call.IsResult || call.Name == "communicate"
}

// shownMessage returns the one message the app shows the user for a call that
// carries a message to the user: message, or output.message when message is
// empty, read the way the app reads it.
func shownMessage(arguments string) string {
	return apptranscript.CommunicateMessageFromArguments(apptranscript.NormalizeCommunicateArguments(json.RawMessage(arguments)))
}

// resultMessages returns all the prose one result-tool call carries: its
// message, and its output.message when that says something else. Arguments
// that are not valid JSON carry no prose.
func resultMessages(arguments string) []string {
	var args struct {
		Message string          `json:"message"`
		Output  json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return nil
	}
	var output struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(args.Output, &output) // output may be absent or not an object
	var out []string
	message := strings.TrimSpace(args.Message)
	if message != "" {
		out = append(out, message)
	}
	if m := strings.TrimSpace(output.Message); m != "" && m != message {
		out = append(out, m)
	}
	return out
}

// labeledDir is one results directory and the prompt version it measures.
type labeledDir struct {
	Label string
	Dir   string
}

// parseLabeled splits a LABEL=VALUE flag value.
func parseLabeled(s string) (string, string, error) {
	label, value, ok := strings.Cut(s, "=")
	label, value = strings.TrimSpace(label), strings.TrimSpace(value)
	if !ok || label == "" || value == "" {
		return "", "", fmt.Errorf("want LABEL=VALUE, got %q", s)
	}
	return label, value, nil
}

func parseLabeledDirs(values []string) ([]labeledDir, error) {
	dirs := make([]labeledDir, 0, len(values))
	for _, v := range values {
		label, dir, err := parseLabeled(v)
		if err != nil {
			return nil, err
		}
		dirs = append(dirs, labeledDir{Label: label, Dir: dir})
	}
	if len(dirs) == 0 {
		return nil, errors.New("at least one --results LABEL=DIR is required")
	}
	return dirs, nil
}

// loadedResult is one run's result and the file it came from.
type loadedResult struct {
	Path   string
	Result probeResult
}

// loadResults reads every result.json under dir: one run's output or a
// whole matrix.
func loadResults(dir string) ([]loadedResult, error) {
	var out []loadedResult
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "result.json" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var res probeResult
		if err := json.Unmarshal(data, &res); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, loadedResult{Path: path, Result: res})
		return nil
	})
	return out, err
}

// proseStats summarizes one prompt version on one model.
type proseStats struct {
	Label              string      `json:"label"`
	Model              string      `json:"model"`
	Runs               int         `json:"runs"`
	Passed             int         `json:"passed"`
	Blocked            int         `json:"blocked"` // runs the gateway or harness stopped; they say nothing about the prompt
	Tasks              int         `json:"tasks"`
	TasksAllPassed     int         `json:"tasks_all_passed"` // tasks that passed on every run
	Messages           int         `json:"messages"`         // root result-tool messages
	MedianMessageWords int         `json:"median_message_words"`
	ToUser             proseCounts `json:"to_user"`
	All                proseCounts `json:"all"`
	ProseErrors        int         `json:"prose_errors"` // runs whose transcripts could not be read
}

// summarizeProse groups runs by label and model and counts their prose.
func summarizeProse(dirs []labeledDir) ([]proseStats, error) {
	type key struct{ label, model string }
	rows := map[key]*proseStats{}
	messageWords := map[key][]int{}
	taskFailed := map[key]map[string]bool{} // probe -> failed on some run
	for _, d := range dirs {
		results, err := loadResults(d.Dir)
		if err != nil {
			return nil, err
		}
		for _, lr := range results {
			res := lr.Result
			k := key{d.Label, res.Model}
			row := rows[k]
			if row == nil {
				row = &proseStats{Label: d.Label, Model: res.Model}
				rows[k] = row
				taskFailed[k] = map[string]bool{}
			}
			row.Runs++
			passed := res.Status == "passed"
			if passed {
				row.Passed++
			} else if res.Status != "failed" {
				row.Blocked++
			}
			taskFailed[k][res.Probe] = taskFailed[k][res.Probe] || !passed
			p, err := extractRunProse(res.StateDir)
			if err != nil {
				row.ProseErrors++
				continue
			}
			for _, msg := range p.ToUser {
				c := countProse(msg)
				row.ToUser.add(c)
				row.Messages++
				messageWords[k] = append(messageWords[k], c.Words)
			}
			for _, piece := range p.All {
				row.All.add(countProse(piece))
			}
		}
	}
	out := make([]proseStats, 0, len(rows))
	for k, row := range rows {
		row.Tasks = len(taskFailed[k])
		for _, failed := range taskFailed[k] {
			if !failed {
				row.TasksAllPassed++
			}
		}
		row.MedianMessageWords = median(messageWords[k])
		out = append(out, *row)
	}
	slices.SortFunc(out, func(a, b proseStats) int {
		if c := strings.Compare(a.Label, b.Label); c != 0 {
			return c
		}
		return strings.Compare(a.Model, b.Model)
	})
	return out, nil
}

func median(xs []int) int {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Sorted(slices.Values(xs))
	return s[len(s)/2]
}

func per1k(n, words int) float64 {
	if words == 0 {
		return 0
	}
	return float64(n) * 1000 / float64(words)
}

// renderProseTable prints one row per label and model for the chosen channel:
// to_user (what the root sent the user) or all (every session's prose).
func renderProseTable(w io.Writer, stats []proseStats, channel string) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "LABEL\tMODEL\tRUNS\tPASSED\tBLOCKED\tTASKS ALL PASSED\tMSGS/RUN\tMEDIAN MSG WORDS\tWORDS\tEM DASH/1K\tX-NOT-Y/1K\tBOLD LABEL/1K\tHEADER/1K\tARROW/1K\tSHOUT/1K\tIDS/1K\tPROSE ERRORS")
	for _, s := range stats {
		c := s.ToUser
		if channel == "all" {
			c = s.All
		}
		msgsPerRun := 0.0
		if s.Runs > 0 {
			msgsPerRun = float64(s.Messages) / float64(s.Runs)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d/%d\t%.1f\t%d\t%d\t%.1f\t%.1f\t%.1f\t%.1f\t%.1f\t%.1f\t%.1f\t%d\n",
			s.Label, s.Model, s.Runs, s.Passed, s.Blocked, s.TasksAllPassed, s.Tasks, msgsPerRun, s.MedianMessageWords, c.Words,
			per1k(c.EmDashes, c.Words), per1k(c.Contrastive, c.Words), per1k(c.BoldLabels, c.Words),
			per1k(c.Headers, c.Words), per1k(c.Arrows, c.Words), per1k(c.Shouting, c.Words),
			per1k(c.OpaqueIDs, c.Words), s.ProseErrors)
	}
	return tw.Flush()
}

func runProseStats(args []string) error {
	fs := flag.NewFlagSet("prose-stats", flag.ContinueOnError)
	var results cmdutil.StringSliceFlag
	fs.Var(&results, "results", "LABEL=DIR of a run or matrix output (repeatable)")
	asJSON := fs.Bool("json", false, "emit JSON with both channels")
	channel := fs.String("channel", "to_user", "prose to tabulate: to_user or all")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *channel != "to_user" && *channel != "all" {
		return errors.New("--channel must be to_user or all")
	}
	dirs, err := parseLabeledDirs(results)
	if err != nil {
		return err
	}
	stats, err := summarizeProse(dirs)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(stats)
	}
	return renderProseTable(os.Stdout, stats, *channel)
}

// runProseCount counts the prose in files, such as prompt sections.
func runProseCount(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: evener-fluency prose-count FILE [FILE ...]")
	}
	for _, path := range args {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		c := countProse(string(data))
		fmt.Printf("%s\twords=%d em_dashes=%d contrastive=%d bold_labels=%d headers=%d arrows=%d shouting=%d opaque_ids=%d\n",
			path, c.Words, c.EmDashes, c.Contrastive, c.BoldLabels, c.Headers, c.Arrows, c.Shouting, c.OpaqueIDs)
	}
	return nil
}
