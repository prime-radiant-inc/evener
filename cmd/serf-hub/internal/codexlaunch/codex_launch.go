package codexlaunch

import (
	"bufio"
	"bytes"
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

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/envvars"
)

type CodexLaunchConfig struct {
	ID              string            `toml:"id"`
	Binary          string            `toml:"binary"`
	WorkingDir      string            `toml:"working_dir"`
	Listen          string            `toml:"listen"`
	Args            []string          `toml:"args"`
	Env             map[string]string `toml:"env"`
	Timeout         time.Duration     `toml:"timeout"`
	BearerToken     string            `toml:"bearer_token"`
	BearerTokenFile string            `toml:"bearer_token_file"`
}

type CodexLauncher struct {
	Mu          sync.Mutex
	configs     map[string]CodexLaunchConfig
	Running     map[string]*LaunchedCodex
	Sources     map[string]appsource.Source
	client      *http.Client
	logOutput   io.Writer
	process     func(string, ...string) launchProcess
	newTicker   func(time.Duration) launchTicker
	withTimeout func(context.Context, time.Duration) (context.Context, context.CancelFunc)
}

type LaunchedCodex struct {
	Cmd      *exec.Cmd
	process  launchProcess
	endpoint string
	// Exited is closed when the launched process exits (cmd.Wait returns). It is
	// a broadcast: any number of observers may select on it, repeatedly, without
	// consuming a single-shot signal.
	Exited <-chan struct{}
	// drainComplete is closed after both app-server pipe forwarders have
	// finished, so shutdown cannot cut off output that was already written.
	drainComplete <-chan struct{}
	// closePipes interrupts a drain whose writer was inherited by a descendant
	// that outlived the app-server. It is idempotent so normal EOF cleanup and a
	// bounded shutdown can safely race.
	closePipes func()
}

type launchProcess interface {
	Cmd() *exec.Cmd
	SetDir(string)
	SetEnv([]string)
	StdoutPipe() (io.ReadCloser, error)
	StderrPipe() (io.ReadCloser, error)
	Start() error
	Wait() error
	Kill() error
}

// execLaunchProcess owns the app-server's pipes instead of borrowing os/exec's.
// Cmd.StdoutPipe hands back a pipe that Cmd.Wait closes the moment the child
// exits — "It is thus incorrect to call Wait before all reads from the pipe
// have completed" — and this launch cannot honour that, because the readiness
// loop needs the exit signal while the scanners are still draining. A pipe the
// launch owns survives Wait, so the app-server's dying words are still there to
// be read after the exit that produced them, and a scanner reaches EOF instead
// of an os.ErrClosed it would have to report as a read failure (kata j27f).
type execLaunchProcess struct {
	cmd *exec.Cmd
	// The child's ends, held only until Start hands them to the child: the
	// parent must drop its copies or no scanner ever sees the EOF that the
	// app-server's exit produces.
	childEnds []*os.File
	// The launch's ends. Closed here only when Start fails and no scanner will
	// ever read them, which is what os/exec does with its own pipes.
	launchEnds []*os.File
}

// closeOnceReadCloser makes the launch's forced drain cutoff safe to race with
// the forwarder's normal deferred Close.
type closeOnceReadCloser struct {
	io.ReadCloser
	once sync.Once
	err  error
}

func (r *closeOnceReadCloser) Close() error {
	r.once.Do(func() { r.err = r.ReadCloser.Close() })
	return r.err
}

func (p *execLaunchProcess) Cmd() *exec.Cmd                     { return p.cmd }
func (p *execLaunchProcess) SetDir(dir string)                  { p.cmd.Dir = dir }
func (p *execLaunchProcess) SetEnv(env []string)                { p.cmd.Env = env }
func (p *execLaunchProcess) StdoutPipe() (io.ReadCloser, error) { return p.pipeTo(&p.cmd.Stdout) }
func (p *execLaunchProcess) StderrPipe() (io.ReadCloser, error) { return p.pipeTo(&p.cmd.Stderr) }

// pipeTo points one of the child's output streams at a pipe this launch owns.
// An *os.File assigned to Cmd.Stdout or Cmd.Stderr is passed straight to the
// child, so os/exec never adopts either end and Wait never closes one.
func (p *execLaunchProcess) pipeTo(stream *io.Writer) (io.ReadCloser, error) {
	launchEnd, childEnd, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	*stream = childEnd
	p.childEnds = append(p.childEnds, childEnd)
	p.launchEnds = append(p.launchEnds, launchEnd)
	return launchEnd, nil
}

// Start hands the write ends to the child and then drops the parent's copies,
// which is what turns the app-server's exit into the EOF a scanner stops on.
// os/exec does this for pipes it made itself; it cannot for pipes it was given.
func (p *execLaunchProcess) Start() error {
	err := p.cmd.Start()
	for _, childEnd := range p.childEnds {
		_ = childEnd.Close()
	}
	p.childEnds = nil
	if err != nil {
		for _, launchEnd := range p.launchEnds {
			_ = launchEnd.Close()
		}
	}
	p.launchEnds = nil
	return err
}

func (p *execLaunchProcess) Wait() error { return p.cmd.Wait() }
func (p *execLaunchProcess) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Kill()
}

type launchTicker interface {
	C() <-chan time.Time
	Stop()
}

type realLaunchTicker struct{ ticker *time.Ticker }

func (t *realLaunchTicker) C() <-chan time.Time { return t.ticker.C }
func (t *realLaunchTicker) Stop()               { t.ticker.Stop() }

var newRequestWithContext = http.NewRequestWithContext

func NewCodexLauncher(configs []CodexLaunchConfig) *CodexLauncher {
	byID := make(map[string]CodexLaunchConfig, len(configs))
	for _, cfg := range configs {
		id := strings.TrimSpace(cfg.ID)
		if id == "" {
			id = "codex"
		}
		cfg.ID = id
		byID[id] = cfg
	}
	return &CodexLauncher{
		configs: byID,
		Running: map[string]*LaunchedCodex{},
		Sources: map[string]appsource.Source{},
		client:  http.DefaultClient,
		// The hub's own log: a launched app-server's output has nowhere else
		// to go, since the hub holds both its pipes to scan them.
		logOutput: os.Stderr,
		process: func(name string, args ...string) launchProcess {
			return &execLaunchProcess{cmd: exec.CommandContext(context.Background(), name, args...)}
		},
		newTicker: func(d time.Duration) launchTicker {
			return &realLaunchTicker{ticker: time.NewTicker(d)}
		},
		withTimeout: context.WithTimeout,
	}
}

func (l *CodexLauncher) EnsureSource(ctx context.Context, sourceID string, sources *appsource.Registry) (appsource.Source, error) {
	l.Mu.Lock()
	defer l.Mu.Unlock()
	if source, ok := l.cachedSourceLocked(sourceID, sources); ok {
		if sources != nil {
			sources.Add(source)
		}
		return source, nil
	}
	cfg, ok := l.configs[sourceID]
	if !ok {
		return nil, appwire.HubLaunchError("codex launch not configured: " + sourceID)
	}
	launched, err := l.launchLocked(ctx, cfg)
	if err != nil {
		return nil, err
	}
	source := appsource.NewCodexSource(appsource.CodexSourceConfig{
		ID:              sourceID,
		Endpoint:        launched.endpoint,
		BearerToken:     cfg.BearerToken,
		BearerTokenFile: cfg.BearerTokenFile,
	}, l.client)
	l.Running[sourceID] = launched
	l.Sources[sourceID] = source
	if sources != nil {
		sources.Add(source)
	}
	return source, nil
}

func (l *CodexLauncher) Manages(sourceID string) bool {
	l.Mu.Lock()
	defer l.Mu.Unlock()
	_, ok := l.configs[sourceID]
	return ok
}

func (l *CodexLauncher) cachedSourceLocked(sourceID string, sources *appsource.Registry) (appsource.Source, bool) {
	source, hasSource := l.Sources[sourceID]
	launched := l.Running[sourceID]
	if !hasSource {
		return nil, false
	}
	if launched != nil && !launchedCodexExited(launched) {
		return source, true
	}
	delete(l.Running, sourceID)
	delete(l.Sources, sourceID)
	if sources != nil {
		sources.Remove(sourceID)
	}
	return nil, false
}

func launchedCodexExited(launched *LaunchedCodex) bool {
	select {
	case <-launched.Exited:
		return true
	default:
		return false
	}
}

func (l *CodexLauncher) Shutdown(ctx context.Context) error {
	l.Mu.Lock()
	running := make([]*LaunchedCodex, 0, len(l.Running))
	for _, launched := range l.Running {
		running = append(running, launched)
	}
	l.Running = map[string]*LaunchedCodex{}
	l.Sources = map[string]appsource.Source{}
	l.Mu.Unlock()

	for _, launched := range running {
		if launched.process != nil {
			_ = launched.process.Kill()
		} else if launched.Cmd.Process != nil {
			_ = launched.Cmd.Process.Kill()
		}
		select {
		case <-launched.Exited:
		case <-ctx.Done():
			if launched.closePipes != nil {
				launched.closePipes()
			}
			return ctx.Err()
		}
		if launched.drainComplete == nil {
			continue
		}
		drainTimer := time.NewTimer(codexDrainGracePeriod)
		select {
		case <-launched.drainComplete:
			stopTimer(drainTimer)
		case <-ctx.Done():
			stopTimer(drainTimer)
			if launched.closePipes != nil {
				launched.closePipes()
			}
			return ctx.Err()
		case <-drainTimer.C:
			if launched.closePipes != nil {
				launched.closePipes()
			}
		}
	}
	return nil
}

// codexDrainGracePeriod gives output already written by the app-server time to
// reach the hub after the direct child exits. When a descendant inherited the
// pipe's write end, the read cannot reach EOF on its own; closing the launch's
// read ends after this bound releases Shutdown without discarding the normal
// final-output path.
const codexDrainGracePeriod = time.Second

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func (l *CodexLauncher) launchLocked(ctx context.Context, cfg CodexLaunchConfig) (*LaunchedCodex, error) {
	binary := strings.TrimSpace(cfg.Binary)
	if binary == "" {
		binary = "codex"
	}
	listen := strings.TrimSpace(cfg.Listen)
	if listen == "" {
		listen = "ws://127.0.0.1:0"
	}
	if !strings.HasPrefix(listen, "ws://") {
		return nil, appwire.HubLaunchError("hub-launched codex app-server requires websocket listen URL")
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	args := buildCodexLaunchArgs(binary, cfg.Args, listen)
	// NOT CommandContext: the launched codex app-server must outlive this
	// call's ctx (the caller owns it via LaunchedCodex). ctx scopes only the
	// readiness wait below; on timeout we kill the process explicitly.
	process := l.process(binary, args...) //nolint:noctx // detached app-server must outlive ctx (see comment)
	process.SetDir(cfg.WorkingDir)
	process.SetEnv(codexLaunchEnv(cfg.Env))
	stdout, err := process.StdoutPipe()
	if err != nil {
		return nil, appwire.HubLaunchError("prepare codex app-server stdout: " + err.Error())
	}
	stderr, err := process.StderrPipe()
	if err != nil {
		_ = stdout.Close()
		return nil, appwire.HubLaunchError("prepare codex app-server stderr: " + err.Error())
	}
	stdout = &closeOnceReadCloser{ReadCloser: stdout}
	stderr = &closeOnceReadCloser{ReadCloser: stderr}
	endpoints := make(chan string, 4)
	if err := process.Start(); err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, appwire.HubLaunchError("start codex app-server: " + err.Error())
	}
	prefix := codexLogPrefix(cfg.ID)
	// The readiness wait below is the only reader endpoints will ever have.
	// Closing this on the way out tells the scanners that, so a later
	// endpoint-shaped line becomes a log line instead of a send nobody will
	// take (kata e1nh).
	launchDone := make(chan struct{})
	launchReturned := false
	closePipes := func() {
		_ = stdout.Close()
		_ = stderr.Close()
	}
	defer func() {
		close(launchDone)
		if !launchReturned {
			closePipes()
		}
	}()
	var forwarders sync.WaitGroup
	forwarders.Add(2)
	drainComplete := make(chan struct{})
	go func() {
		forwarders.Wait()
		close(drainComplete)
	}()
	go func() {
		defer forwarders.Done()
		forwardCodexPipe(stdout, endpoints, launchDone, l.logOutput, prefix)
	}()
	go func() {
		defer forwarders.Done()
		forwardCodexPipe(stderr, endpoints, launchDone, l.logOutput, prefix)
	}()
	// exitErr is published before close(exited); a receive on exited
	// happens-after the close, so reading exitErr after the receive is race-free.
	exited := make(chan struct{})
	var exitErr error
	go func() {
		exitErr = process.Wait()
		close(exited)
	}()

	waitCtx, cancel := l.withTimeout(ctx, timeout)
	defer cancel()
	endpoint := configuredCodexEndpoint(listen)
	ticker := l.newTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if endpoint != "" && CodexReady(waitCtx, l.client, endpoint) {
			launchReturned = true
			return &LaunchedCodex{
				Cmd:           process.Cmd(),
				process:       process,
				endpoint:      endpoint,
				Exited:        exited,
				drainComplete: drainComplete,
				closePipes:    closePipes,
			}, nil
		}
		select {
		case next := <-endpoints:
			if next != "" {
				endpoint = next
			}
		case <-exited:
			if exitErr != nil {
				return nil, appwire.HubLaunchError("codex app-server exited before ready: " + exitErr.Error())
			}
			return nil, appwire.HubLaunchError("codex app-server exited before ready")
		case <-ticker.C():
		case <-waitCtx.Done():
			_ = process.Kill()
			return nil, codexReadyWaitError(waitCtx)
		}
	}
}

// codexReadyWaitError says which way a ready-wait that never saw the
// app-server come up was stopped. Its context is done for two unrelated
// reasons — the launch's readiness budget elapsed, or the caller went away —
// and only the first is a timeout. Calling the second one sends an operator
// triaging it after a slow machine or a too-short launch timeout, when nothing
// was slow and nobody is waiting for the app-server any more (kata f9hr).
//
// The wait runs under the caller's context on every hub path that reaches it:
// EnsureSource is called from thread lifecycle handlers carrying a live
// request context — r.Context() on the REST spawn, the websocket connection's
// ctx (which the keepalive cancels) on the RPC one — so a client that drops
// mid-launch lands here.
//
// ctx.Err() separates the two outright, the same way the daemon path's
// launchCheckWaitError does: Canceled is the caller walking away,
// DeadlineExceeded is time genuinely running out — the launch's own budget, or
// a deadline the caller brought with it.
//
// Both stay an appwire.HubLaunchError, the discriminator clients read to
// headline the failure as a session that would not start. The label changes;
// the family of failure does not.
func codexReadyWaitError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return appwire.HubLaunchError("codex app-server launch canceled waiting for ready")
	}
	return appwire.HubLaunchError("codex app-server timed out waiting for ready")
}

func buildCodexLaunchArgs(binary string, configured []string, listen string) []string {
	var args []string
	if configured != nil {
		args = append(args, configured...)
	} else if !strings.Contains(filepath.Base(binary), "codex-app-server") {
		args = append(args, "app-server")
	}
	if !argsContainFlag(args, "--listen") {
		args = append(args, "--listen", listen)
	}
	return args
}

func argsContainFlag(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag || strings.HasPrefix(arg, flag+"=") {
			return true
		}
	}
	return false
}

func codexLaunchEnv(overrides map[string]string) []string {
	env := append([]string{}, os.Environ()...)
	env = append(env, envvars.SERFHubSpawnedCodex.Assignment("1"))
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

// codexLogPrefix attributes a forwarded line to the launch that produced it,
// so one hub log carrying several app-servers says which one spoke.
func codexLogPrefix(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "[codex]"
	}
	return "[codex:" + id + "]"
}

// forwardCodexPipe scans one of the app-server's pipes to its end and then
// gives up the launch's end of it. The launch owns these pipes so that Cmd.Wait
// cannot close them out from under a scan in progress (kata j27f), which leaves
// closing them to the reader that has finished with one. "Its end" is now the
// pipe's real EOF — every writer gone, the app-server's own words included —
// rather than the moment Wait pulled the descriptor away.
func forwardCodexPipe(r io.ReadCloser, endpoints chan<- string, launchDone <-chan struct{}, out io.Writer, prefix string) {
	defer r.Close() //nolint:errcheck // a drained read end's close has no actionable failure
	scanCodexEndpoint(r, endpoints, launchDone, out, prefix)
}

// maxCodexLogLine bounds how much of one app-server line the launch keeps. The
// value is the limit that has always been in force here — bufio.Scanner's
// default token size, which every line the app-server writes on purpose fits
// inside — so nothing that reaches the hub log today is cut by it. What changes
// past the bound is only what a longer line costs: the rest of it, and not the
// pipe (kata jqbb).
const maxCodexLogLine = 64 << 10

func scanCodexEndpoint(r io.Reader, endpoints chan<- string, launchDone <-chan struct{}, out io.Writer, prefix string) {
	reader := bufio.NewReaderSize(r, maxCodexLogLine)
	for {
		line, dropped, err := readCodexLogLine(reader)
		if err == nil || len(line) > 0 || dropped > 0 {
			forwardCodexLine(line, dropped, endpoints, launchDone, out, prefix)
		}
		if err == nil {
			continue
		}
		// A pipe that ended and a pipe that can no longer be read both stop the
		// scan, and only the second means the launch has lost the ability to
		// hear an app-server that may still be talking. Unannounced, that reads
		// as an app-server that went quiet, which is the one thing it does not
		// mean (kata e1nh).
		if !errors.Is(err, io.EOF) {
			_, _ = fmt.Fprintf(out, "%s app-server output ended early: %v\n", prefix, err)
		}
		return
	}
}

// forwardCodexLine puts one line of app-server output where it belongs: the
// launch's readiness wait, if it is an announcement the wait is still there to
// take, and the hub log otherwise. The hub holds both of the app-server's pipes
// in order to scan them, so a line dropped here — a bind failure, a crash, a
// warning — is a line nobody will ever see (kata d35w).
func forwardCodexLine(line []byte, dropped int, endpoints chan<- string, launchDone <-chan struct{}, out io.Writer, prefix string) {
	if dropped > 0 {
		// A line the launch had to cut is never taken as an announcement:
		// consuming it as one would swallow the only record that it was cut.
		_, _ = fmt.Fprintf(out, "%s %s [truncated: %d bytes dropped from this line]\n", prefix, line, dropped)
		return
	}
	if endpoint, ok := ParseCodexEndpoint(string(line)); ok && deliverCodexEndpoint(endpoints, launchDone, endpoint) {
		return
	}
	_, _ = fmt.Fprintf(out, "%s %s\n", prefix, line)
}

// readCodexLogLine reads one newline-framed line, keeping at most
// maxCodexLogLine bytes of it and reporting how many further bytes of the same
// line it read and dropped. Consuming the remainder is the whole point: a line
// the launch merely stopped reading leaves the pipe to fill, and a full pipe
// blocks the app-server writing into it, so one overlong line would silence the
// process as well as the log (kata jqbb).
//
// The returned line aliases the reader's buffer and stays valid only until the
// next read from it, which is why the caller forwards it before looping.
func readCodexLogLine(reader *bufio.Reader) (line []byte, dropped int, err error) {
	line, err = reader.ReadSlice('\n')
	if !errors.Is(err, bufio.ErrBufferFull) {
		return dropCodexLineEnd(line), 0, err
	}
	kept := bytes.Clone(line)
	pendingCR := false
	for {
		rest, restErr := reader.ReadSlice('\n')
		if pendingCR {
			if len(rest) == 0 || rest[0] != '\n' {
				dropped++
			}
			pendingCR = false
		}
		if len(rest) > 0 && rest[0] == '\n' && bytes.HasSuffix(kept, []byte{'\r'}) {
			kept = kept[:len(kept)-1]
		}
		if errors.Is(restErr, bufio.ErrBufferFull) {
			if bytes.HasSuffix(rest, []byte{'\r'}) {
				rest = rest[:len(rest)-1]
				pendingCR = true
			}
			dropped += len(rest)
			continue
		}
		dropped += len(dropCodexLineEnd(rest))
		return kept, dropped, restErr
	}
}

// dropCodexLineEnd strips the framing that bufio.ScanLines strips, so an
// app-server writing CRLF does not carry a carriage return into the hub log.
func dropCodexLineEnd(line []byte) []byte {
	if !bytes.HasSuffix(line, []byte("\n")) {
		return line
	}
	return bytes.TrimSuffix(line[:len(line)-1], []byte("\r"))
}

// deliverCodexEndpoint offers an announcement to the launch's readiness wait
// and reports whether the wait took it.
//
// Only that wait ever receives from endpoints, and it is gone the moment
// launchLocked returns. ParseCodexEndpoint accepts any line carrying a ws://
// URL, so an app-server that keeps mentioning its own address goes on
// producing announcements for a reader that no longer exists: past the
// channel's buffer, a blocking send parks the scanner goroutine for good, and
// the app-server's only log record dies with it (kata e1nh).
//
// launchDone says the wait is gone. An announcement that arrives after it is
// just another line for the log, which is where the caller sends everything a
// launched app-server says once the launch itself is settled.
func deliverCodexEndpoint(endpoints chan<- string, launchDone <-chan struct{}, endpoint string) bool {
	// Checked first so a settled launch is decided, not raced against a
	// buffer slot that happens to be free.
	select {
	case <-launchDone:
		return false
	default:
	}
	select {
	case endpoints <- endpoint:
		return true
	case <-launchDone:
		return false
	}
}

func ParseCodexEndpoint(line string) (string, bool) {
	if endpoint, ok := parseCodexEndpointJSON(line); ok {
		return endpoint, true
	}
	idx := strings.Index(line, "ws://")
	if idx < 0 {
		return "", false
	}
	raw := line[idx:]
	if fields := strings.Fields(raw); len(fields) > 0 {
		raw = fields[0]
	}
	raw = strings.TrimRight(raw, ".,)")
	return validCodexEndpoint(raw)
}

func parseCodexEndpointJSON(line string) (string, bool) {
	var payload struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		return "", false
	}
	return validCodexEndpoint(payload.Endpoint)
}

func validCodexEndpoint(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "ws" || u.Host == "" {
		return "", false
	}
	return raw, true
}

func configuredCodexEndpoint(listen string) string {
	u, err := url.Parse(listen)
	if err != nil || u.Scheme != "ws" || strings.HasSuffix(u.Host, ":0") {
		return ""
	}
	return listen
}

func CodexReady(ctx context.Context, client *http.Client, endpoint string) bool {
	readyURL, err := codexReadyURL(endpoint)
	if err != nil {
		return false
	}
	req, err := newRequestWithContext(ctx, http.MethodGet, readyURL, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close() //nolint:errcheck // response body close on read path; error is not actionable
	return resp.StatusCode == http.StatusOK
}

func codexReadyURL(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	if u.Scheme != "ws" {
		return "", fmt.Errorf("unsupported codex endpoint scheme: %s", u.Scheme)
	}
	u.Scheme = "http"
	u.Path = "/readyz"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}
