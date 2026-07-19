// Command serf-transcript-v2-upgrade converts selected legacy transcript v1
// files into the semantic-only transcript v2 format.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"primeradiant.com/serf/agent/transcript"
)

const legacyFormatVersion = 1

type options struct {
	root   string
	cutoff time.Time
	apply  bool
}

type summary struct {
	candidates      int
	eligible        int
	upgraded        int
	skippedCurrent  int
	skippedOld      int
	removedAPICalls int
	errors          int
}

type preparedTranscript struct {
	path            string
	data            []byte
	original        os.FileInfo
	removedAPICalls int
}

func main() {
	os.Exit(run(os.Args[1:], time.Now(), os.Stdout, os.Stderr))
}

func run(args []string, now time.Time, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("serf-transcript-v2-upgrade", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", "", "projects root containing */sessions/*.transcript.jsonl (required)")
	since := flags.Duration("since", 120*time.Hour, "upgrade v1 transcripts modified within this rolling window")
	apply := flags.Bool("apply", false, "replace eligible transcripts and retain .v1.bak backups")
	flags.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: serf-transcript-v2-upgrade --root <projects-root> [--since 120h] [--apply]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "serf-transcript-v2-upgrade: positional arguments are not accepted")
		return 2
	}
	if strings.TrimSpace(*root) == "" {
		_, _ = fmt.Fprintln(stderr, "serf-transcript-v2-upgrade: --root is required")
		return 2
	}
	if *since <= 0 {
		_, _ = fmt.Fprintln(stderr, "serf-transcript-v2-upgrade: --since must be positive")
		return 2
	}

	result, failures, err := upgradeRoot(options{
		root:   *root,
		cutoff: now.Add(-*since),
		apply:  *apply,
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "serf-transcript-v2-upgrade: %v\n", err)
		return 1
	}
	for _, failure := range failures {
		_, _ = fmt.Fprintf(stderr, "serf-transcript-v2-upgrade: %v\n", failure)
	}
	_, _ = fmt.Fprintf(stdout,
		"candidates=%d eligible=%d upgraded=%d skipped_current=%d skipped_old=%d removed_api_calls=%d errors=%d\n",
		result.candidates,
		result.eligible,
		result.upgraded,
		result.skippedCurrent,
		result.skippedOld,
		result.removedAPICalls,
		result.errors,
	)
	if result.errors != 0 {
		return 1
	}
	return 0
}

func upgradeRoot(opts options) (summary, []error, error) {
	paths, err := discoverTranscripts(opts.root)
	if err != nil {
		return summary{}, nil, err
	}

	result := summary{candidates: len(paths)}
	var failures []error
	for _, path := range paths {
		version, info, err := inspectTranscriptHeader(path)
		if err != nil {
			result.errors++
			failures = append(failures, fmt.Errorf("%s: %w", path, err))
			continue
		}
		if version == transcript.FormatVersion {
			result.skippedCurrent++
			continue
		}
		if version != legacyFormatVersion {
			result.errors++
			failures = append(failures, fmt.Errorf("%s: unsupported transcript format_version %d", path, version))
			continue
		}
		if info.ModTime().Before(opts.cutoff) {
			result.skippedOld++
			continue
		}

		result.eligible++
		if err := requireUnusedBackup(path); err != nil {
			result.errors++
			failures = append(failures, err)
			continue
		}
		prepared, err := prepareTranscript(path)
		if err != nil {
			result.errors++
			failures = append(failures, err)
			continue
		}
		result.removedAPICalls += prepared.removedAPICalls
		if !opts.apply {
			continue
		}
		if err := replaceTranscript(prepared); err != nil {
			result.errors++
			failures = append(failures, err)
			continue
		}
		result.upgraded++
	}
	return result, failures, nil
}

func discoverTranscripts(root string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Base(filepath.Dir(path)) != "sessions" || !strings.HasSuffix(entry.Name(), ".transcript.jsonl") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discover transcripts below %s: %w", root, err)
	}
	sort.Strings(paths)
	return paths, nil
}

func inspectTranscriptHeader(path string) (int, os.FileInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, nil, fmt.Errorf("open: %w", err)
	}
	defer file.Close() //nolint:errcheck
	info, err := file.Stat()
	if err != nil {
		return 0, nil, fmt.Errorf("stat: %w", err)
	}
	line, complete, bytesRead, err := transcript.ReadLine(bufio.NewReader(file), transcript.DefaultMaxLineBytes)
	if err != nil {
		return 0, nil, fmt.Errorf("record 1: %w", err)
	}
	if !complete {
		if bytesRead == 0 {
			return 0, nil, errors.New("empty transcript")
		}
		return 0, nil, errors.New("record 1: incomplete transcript header")
	}
	var boundary struct {
		Kind          string `json:"kind"`
		FormatVersion int    `json:"format_version"`
	}
	if err := json.Unmarshal(line, &boundary); err != nil {
		return 0, nil, fmt.Errorf("record 1: decode transcript header boundary: %w", err)
	}
	if boundary.Kind != "header" {
		return 0, nil, fmt.Errorf("record 1: kind %q is not a transcript header", boundary.Kind)
	}
	if boundary.FormatVersion == transcript.FormatVersion {
		if _, err := transcript.DecodeHeader(line); err != nil {
			return 0, nil, fmt.Errorf("record 1: %w", err)
		}
	}
	return boundary.FormatVersion, info, nil
}

func prepareTranscript(path string) (preparedTranscript, error) {
	file, err := os.Open(path)
	if err != nil {
		return preparedTranscript{}, fmt.Errorf("%s: open: %w", path, err)
	}
	defer file.Close() //nolint:errcheck
	info, err := file.Stat()
	if err != nil {
		return preparedTranscript{}, fmt.Errorf("%s: stat: %w", path, err)
	}
	reader := bufio.NewReader(file)

	headerLine, complete, bytesRead, err := transcript.ReadLine(reader, transcript.DefaultMaxLineBytes)
	if err != nil {
		return preparedTranscript{}, fmt.Errorf("%s: record 1: %w", path, err)
	}
	if !complete {
		if bytesRead == 0 {
			return preparedTranscript{}, fmt.Errorf("%s: empty transcript", path)
		}
		return preparedTranscript{}, fmt.Errorf("%s: record 1: incomplete transcript header", path)
	}
	convertedHeader, err := convertHeader(headerLine)
	if err != nil {
		return preparedTranscript{}, fmt.Errorf("%s: record 1: %w", path, err)
	}

	var output bytes.Buffer
	output.Write(convertedHeader)
	output.WriteByte('\n')
	nextSeq := 0
	removedAPICalls := 0
	for recordNumber := 2; ; recordNumber++ {
		line, complete, bytesRead, err := transcript.ReadLine(reader, transcript.DefaultMaxLineBytes)
		if err != nil {
			return preparedTranscript{}, fmt.Errorf("%s: record %d: %w", path, recordNumber, err)
		}
		if !complete {
			if bytesRead == 0 {
				break
			}
			return preparedTranscript{}, fmt.Errorf("%s: record %d: incomplete final record", path, recordNumber)
		}

		var boundary struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(line, &boundary); err != nil {
			return preparedTranscript{}, fmt.Errorf("%s: record %d: decode record boundary: %w", path, recordNumber, err)
		}
		switch boundary.Kind {
		case "entry":
			entry, err := transcript.DecodeEntry(line)
			if err != nil {
				return preparedTranscript{}, fmt.Errorf("%s: record %d: %w", path, recordNumber, err)
			}
			entry.Seq = nextSeq
			converted, err := json.Marshal(entry)
			if err != nil {
				return preparedTranscript{}, fmt.Errorf("%s: record %d: encode entry: %w", path, recordNumber, err)
			}
			if _, err := transcript.DecodeEntry(converted); err != nil {
				return preparedTranscript{}, fmt.Errorf("%s: record %d: validate converted entry: %w", path, recordNumber, err)
			}
			output.Write(converted)
			output.WriteByte('\n')
			nextSeq++
		case "api_call":
			removedAPICalls++
		default:
			return preparedTranscript{}, fmt.Errorf("%s: record %d: unsupported record kind %q", path, recordNumber, boundary.Kind)
		}
	}

	return preparedTranscript{
		path:            path,
		data:            output.Bytes(),
		original:        info,
		removedAPICalls: removedAPICalls,
	}, nil
}

func convertHeader(line []byte) ([]byte, error) {
	var boundary struct {
		Kind          string `json:"kind"`
		FormatVersion int    `json:"format_version"`
	}
	if err := json.Unmarshal(line, &boundary); err != nil {
		return nil, fmt.Errorf("decode transcript header boundary: %w", err)
	}
	if boundary.Kind != "header" || boundary.FormatVersion != legacyFormatVersion {
		return nil, fmt.Errorf("require transcript header with format_version %d", legacyFormatVersion)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return nil, fmt.Errorf("decode transcript header: %w", err)
	}
	version, err := json.Marshal(transcript.FormatVersion)
	if err != nil {
		return nil, fmt.Errorf("encode transcript format version: %w", err)
	}
	fields["format_version"] = version
	converted, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("encode transcript header: %w", err)
	}
	if _, err := transcript.DecodeHeader(converted); err != nil {
		return nil, fmt.Errorf("validate converted transcript header: %w", err)
	}
	return converted, nil
}

func replaceTranscript(prepared preparedTranscript) error {
	if err := requireUnusedBackup(prepared.path); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(prepared.path), ".serf-transcript-v2-*")
	if err != nil {
		return fmt.Errorf("%s: create temporary transcript: %w", prepared.path, err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath) //nolint:errcheck

	if err := temporary.Chmod(prepared.original.Mode().Perm()); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("%s: chmod temporary transcript: %w", prepared.path, err)
	}
	if _, err := temporary.Write(prepared.data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("%s: write temporary transcript: %w", prepared.path, err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("%s: sync temporary transcript: %w", prepared.path, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("%s: close temporary transcript: %w", prepared.path, err)
	}

	current, err := os.Stat(prepared.path)
	if err != nil {
		return fmt.Errorf("%s: restat original transcript: %w", prepared.path, err)
	}
	if !os.SameFile(prepared.original, current) || prepared.original.Size() != current.Size() || !prepared.original.ModTime().Equal(current.ModTime()) {
		return fmt.Errorf("%s: transcript changed during conversion", prepared.path)
	}

	backupPath := prepared.path + ".v1.bak"
	if err := os.Rename(prepared.path, backupPath); err != nil {
		return fmt.Errorf("%s: create v1 backup: %w", prepared.path, err)
	}
	if err := os.Rename(temporaryPath, prepared.path); err != nil {
		if restoreErr := os.Rename(backupPath, prepared.path); restoreErr != nil {
			return fmt.Errorf("%s: publish v2 transcript: %w; restore v1 backup: %w", prepared.path, err, restoreErr)
		}
		return fmt.Errorf("%s: publish v2 transcript: %w", prepared.path, err)
	}
	return nil
}

func requireUnusedBackup(path string) error {
	backupPath := path + ".v1.bak"
	_, err := os.Lstat(backupPath)
	switch {
	case err == nil:
		return fmt.Errorf("%s: backup already exists", backupPath)
	case errors.Is(err, os.ErrNotExist):
		return nil
	default:
		return fmt.Errorf("%s: inspect backup: %w", backupPath, err)
	}
}
