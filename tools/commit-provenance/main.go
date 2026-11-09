// Command commit-provenance experiments with metadata inside Git commit objects.
// It is not installed or used by the Evener runtime.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"unicode"
	"unicode/utf8"

	"primeradiant.com/evener/identifier"
)

var (
	errInvalidRecord = errors.New("provenance requires schema 1 and four nonempty single-line UTF-8 fields")
	errSignedCommit  = errors.New("refusing to change a signed commit, metadata must be inserted before signing")
)

type provenance struct {
	Schema        int    `json:"schema"`
	EvenerVersion string `json:"evener_version"`
	Model         string `json:"model"`
	Host          string `json:"host"`
	SessionID     string `json:"session_id"`
}

func stamp(repo, revision string, record provenance) (string, error) {
	if record.Schema != 1 {
		return "", errInvalidRecord
	}
	for _, field := range []string{record.EvenerVersion, record.Model, record.Host, record.SessionID} {
		if strings.TrimSpace(field) == "" || !utf8.ValidString(field) || strings.IndexFunc(field, unicode.IsControl) >= 0 {
			return "", errInvalidRecord
		}
	}
	oid, err := gitOutput(repo, nil, "rev-parse", "--verify", "--end-of-options", revision+"^{commit}")
	if err != nil {
		return "", err
	}
	raw, err := gitOutput(repo, nil, "cat-file", "commit", strings.TrimSpace(string(oid)))
	if err != nil {
		return "", err
	}
	headers, message, ok := bytes.Cut(raw, []byte("\n\n"))
	if !ok {
		return "", fmt.Errorf("commit has no header separator")
	}
	data, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	line := append([]byte("evener-provenance "), data...)
	alreadyPresent := false
	for _, header := range bytes.Split(headers, []byte("\n")) {
		if bytes.HasPrefix(header, []byte("gpgsig ")) || bytes.HasPrefix(header, []byte("gpgsig-sha256 ")) {
			return "", errSignedCommit
		}
		alreadyPresent = alreadyPresent || bytes.Equal(header, line)
	}
	if alreadyPresent {
		return strings.TrimSpace(string(oid)), nil
	}
	var out bytes.Buffer
	out.Write(headers)
	out.WriteByte('\n')
	out.Write(line)
	out.WriteString("\n\n")
	out.Write(message)
	stamped, err := gitOutput(repo, out.Bytes(), "hash-object", "-t", "commit", "-w", "--stdin")
	return strings.TrimSpace(string(stamped)), err
}

func gitOutput(repo string, input []byte, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Env = append(identifier.FilteredGitEnvironment(os.Environ()), "GIT_NO_REPLACE_OBJECTS=1")
	cmd.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("commit-provenance", flag.ContinueOnError)
	flags.SetOutput(stderr)
	repo := flags.String("repo", ".", "Git repository to experiment in")
	revision := flags.String("commit", "", "source commit, required")
	record := provenance{Schema: 1}
	flags.StringVar(&record.EvenerVersion, "evener-version", "", "executing Evener build version, required")
	flags.StringVar(&record.Model, "model", "", "provider/model identifier, required")
	flags.StringVar(&record.Host, "host", "", "execution host, required, becomes visible to repository readers")
	flags.StringVar(&record.SessionID, "session", "", "contributing session ID, required")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: commit-provenance -commit REV -evener-version VERSION -model PROVIDER/MODEL -host HOST -session ID [-repo PATH]")
		fmt.Fprintln(stderr, "Prototype: writes an unreferenced commit object and prints its ID. Leaves refs, index, worktree and hooks unchanged. Refuses signed commits. Use disposable repositories.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *revision == "" || flags.NArg() != 0 {
		flags.Usage()
		return 2
	}
	oid, err := stamp(*repo, *revision, record)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if _, err := fmt.Fprintln(stdout, oid); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
