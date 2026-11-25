package agent

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/cappedoutput"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/execsupport/orphanpipe"
	"primeradiant.com/evener/execsupport/procgroup"
)

const (
	harnessFeedbackToolName         = "harness_feedback"
	defaultHarnessFeedbackTimeout   = 10 * time.Second
	harnessFeedbackCommandWaitDelay = time.Second
	harnessFeedbackStderrCapBytes   = 512
)

// HarnessFeedbackConfig configures runtime-only sinks for the harness_feedback
// tool. Leaving all sinks empty omits the tool completely (zero schema or
// prompt cost). Never persisted in session metadata or transcripts.
type HarnessFeedbackConfig struct {
	WebhookURL string        // HTTP(S) webhook URL (e.g. Slack incoming webhook); secret.
	Command    string        // Shell command (`sh -c`) receiving the JSON report on stdin.
	FilePath   string        // JSONL file path where reports are appended.
	Timeout    time.Duration // Per-sink delivery bound; zero uses 10s.
}

// Enabled reports whether at least one feedback sink is configured.
func (c HarnessFeedbackConfig) Enabled() bool {
	return strings.TrimSpace(c.WebhookURL) != "" ||
		strings.TrimSpace(c.Command) != "" ||
		strings.TrimSpace(c.FilePath) != ""
}

func (c HarnessFeedbackConfig) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return defaultHarnessFeedbackTimeout
}

// ValidateHarnessFeedbackWebhookURL verifies that raw is empty or an HTTP(S)
// URL with a non-empty host. Errors never echo raw because incoming webhook
// URLs carry embedded bearer tokens.
func ValidateHarnessFeedbackWebhookURL(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return errors.New("harness feedback webhook URL is malformed")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("harness feedback webhook URL must use http or https")
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return errors.New("harness feedback webhook URL must include a host")
	}
	return nil
}

type harnessFeedbackPayload struct {
	Text       string `json:"text"`
	Timestamp  string `json:"timestamp"`
	SessionID  string `json:"session_id"`
	Model      string `json:"model,omitempty"`
	Provider   string `json:"provider,omitempty"`
	AgentName  string `json:"agent_name,omitempty"`
	WorkingDir string `json:"working_dir,omitempty"`
	Summary    string `json:"summary"`
	Details    string `json:"details"`
	Category   string `json:"category,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
}

func registerHarnessFeedbackTool(reg *tool.Registry, s *Session) error {
	if !s.cfg.HarnessFeedback.Enabled() {
		return nil
	}
	return reg.Register(tool.RegisteredTool{
		Definition: tool.DefHarnessFeedback(),
		ReadOnly:   false,
		Exec:       s.execHarnessFeedback,
	})
}

func (s *Session) execHarnessFeedback(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
	summary := strings.TrimSpace(stringArg(args, "summary"))
	details := strings.TrimSpace(stringArg(args, "details"))
	if summary == "" || details == "" {
		return nil, errors.New("summary and details must be non-empty")
	}
	payload := s.buildHarnessFeedbackPayload(
		env, summary, details,
		strings.TrimSpace(stringArg(args, "category")),
		strings.TrimSpace(stringArg(args, "tool_name")),
	)
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode harness feedback: %w", err)
	}
	data = append(data, '\n')
	body := data[:len(data)-1]

	type sinkJob struct {
		name string
		run  func() error
	}
	cfg := s.cfg.HarnessFeedback
	timeout := cfg.timeout()
	var jobs []sinkJob
	if filePath := strings.TrimSpace(cfg.FilePath); filePath != "" {
		jobs = append(jobs, sinkJob{"file", func() error { return appendHarnessFeedbackJSONL(filePath, data) }})
	}
	if command := strings.TrimSpace(cfg.Command); command != "" {
		jobs = append(jobs, sinkJob{"command", func() error { return runHarnessFeedbackCommand(ctx, command, data, timeout) }})
	}
	if webhookURL := strings.TrimSpace(cfg.WebhookURL); webhookURL != "" {
		// Operator-configured host telemetry (the model controls neither the
		// URL nor sees the response), so it stays usable under --sandbox-net off.
		jobs = append(jobs, sinkJob{"webhook", func() error { return postHarnessFeedbackWebhook(ctx, webhookURL, body, timeout) }})
	}

	results := make([]error, len(jobs))
	if len(jobs) == 1 {
		results[0] = jobs[0].run()
	} else {
		var wg sync.WaitGroup
		for i, job := range jobs {
			wg.Add(1)
			go func(idx int, fn func() error) {
				defer wg.Done()
				results[idx] = fn()
			}(i, job.run)
		}
		wg.Wait()
	}

	var delivered, errs []string
	for i, job := range jobs {
		if results[i] != nil {
			errs = append(errs, fmt.Sprintf("%s sink: %v", job.name, results[i]))
		} else {
			delivered = append(delivered, job.name)
		}
	}
	if len(errs) > 0 {
		return nil, errors.New("harness_feedback delivery failed: " + strings.Join(errs, "; "))
	}
	return map[string]any{"status": "delivered", "sinks": delivered}, nil
}

func (s *Session) buildHarnessFeedbackPayload(env execenv.ExecutionEnvironment, summary, details, category, toolName string) harnessFeedbackPayload {
	var model, prov string
	if prof := s.currentProfile(); prof != nil {
		model, prov = prof.Model(), prof.ID()
	}
	agentName := cmp.Or(strings.TrimSpace(s.cfg.spawn.agentType), strings.TrimSpace(s.cfg.AgentName), defaultAgentName)
	workingDir := ""
	if env != nil {
		if wd := strings.TrimSpace(env.WorkingDirectory()); wd != "" {
			workingDir = filepath.Base(filepath.Clean(wd))
		}
	}
	header := "[harness_feedback]"
	if category != "" {
		header = "[harness_feedback:" + category + "]"
	}
	var meta []string
	if toolName != "" {
		meta = append(meta, "tool="+toolName)
	}
	if prov != "" && model != "" {
		meta = append(meta, "model="+prov+"/"+model)
	} else if model != "" {
		meta = append(meta, "model="+model)
	}
	meta = append(meta, "agent="+agentName)
	if s.id != "" {
		meta = append(meta, "session="+s.id)
	}
	if workingDir != "" {
		meta = append(meta, "dir="+workingDir)
	}
	text := fmt.Sprintf("%s %s (%s)\n%s", header, summary, strings.Join(meta, ", "), details)

	return harnessFeedbackPayload{
		Text:       text,
		Timestamp:  s.sclock().Now().UTC().Format(time.RFC3339),
		SessionID:  s.id,
		Model:      model,
		Provider:   prov,
		AgentName:  agentName,
		WorkingDir: workingDir,
		Summary:    summary,
		Details:    details,
		Category:   category,
		ToolName:   toolName,
	}
}

var harnessFeedbackFileMu sync.Mutex

func appendHarnessFeedbackJSONL(path string, line []byte) error {
	harnessFeedbackFileMu.Lock()
	defer harnessFeedbackFileMu.Unlock()

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(line)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func runHarnessFeedbackCommand(ctx context.Context, command string, stdin []byte, timeout time.Duration) error {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "sh", "-c", command)
	cmd.Env = sandbox.ScrubSecretEnv(os.Environ())
	cmd.Stdin = bytes.NewReader(stdin)
	stderr := cappedoutput.New(harnessFeedbackStderrCapBytes)
	cmd.Stdout = io.Discard
	cmd.Stderr = stderr
	cmd.WaitDelay = harnessFeedbackCommandWaitDelay
	cmd.SysProcAttr = procgroup.SysProcAttr()
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			procgroup.Kill(cmd.Process.Pid)
		}
		return nil
	}

	runErr := orphanpipe.ChildErr(cmd, cmd.Run())
	if cmd.Process != nil {
		procgroup.KillGroupAfterReap(cmd.Process.Pid)
	}
	if runErr != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%w (%s)", runErr, msg)
		}
		return runErr
	}
	return nil
}

func postHarnessFeedbackWebhook(ctx context.Context, rawURL string, payload []byte, timeout time.Duration) error {
	if err := ValidateHarnessFeedbackWebhookURL(rawURL); err != nil {
		return err
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, rawURL, bytes.NewReader(payload))
	if err != nil {
		return errors.New("could not construct webhook request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "evener")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if uerr, ok := errors.AsType[*url.Error](err); ok && uerr.Err != nil {
			return fmt.Errorf("request failed: %s", redactFeedbackURL(uerr.Err.Error(), rawURL))
		}
		return fmt.Errorf("request failed: %s", redactFeedbackURL(err.Error(), rawURL))
	}
	defer resp.Body.Close() //nolint:errcheck
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func redactFeedbackURL(msg, rawURL string) string {
	msg = strings.ReplaceAll(msg, rawURL, "<redacted>")
	if parsed, err := url.Parse(rawURL); err == nil {
		for _, part := range []string{parsed.RequestURI(), parsed.Path, parsed.RawQuery} {
			if len(part) > 1 {
				msg = strings.ReplaceAll(msg, part, "<redacted>")
			}
		}
	}
	return msg
}
