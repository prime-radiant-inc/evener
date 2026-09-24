// Command turncpu measures how much CPU an evener session burns per tool
// round as its history grows. It serves a scripted, streaming
// chat-completions provider, points an isolated providers.toml at it, runs the
// real evener binary against it, and answers every model round with streamed
// reasoning and text deltas plus a tool call whose output is a sizeable file.
// Each turn runs --rounds tool rounds and then ends with communicate.
//
// For each round it records the evener child's CPU time (utime+stime from
// /proc) consumed between answering one round and receiving the next
// request: the daemon-side work of one tool round (stream consumption, event
// emission, tool execution, transcript write, context management, request
// encoding). Round 0 of the first turn carries startup and is not reported.
//
// With --serve it runs `evener serve` instead of a one-shot prompt and drives
// --turns turns over AppWire with a subscribed client, which is how a
// hub-attached daemon runs; per-turn means then show whether a round costs
// more as the session's total history grows.
//
// With --delegates N the root's first N rounds spawn background delegates,
// each running --child-rounds rounds of the same script (requests are told
// apart by their session affinity headers). Rounds then interleave across
// sessions, so the per-round figures include the children's work; the whole
// process's CPU and the total model rounds are printed as well.
//
// Everything the child touches (HOME, XDG dirs, TMPDIR, the host temp bases
// its crashed-scratch sweep walks) lives under --out.
//
// Usage:
//
//	go build -o /tmp/evener ./cmd/evener
//	go run ./test/e2e/turncpu --evener /tmp/evener [--serve] [--turns 1] [--rounds 200] [--delegates 0] [--probes 0] [--payload-kb 20] [--context-window N] [--cpu-profile] [--out dir]
//
// Then, with --cpu-profile: go tool pprof -top -cum /tmp/evener <out>/cpu.pprof
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/envvars"
)

const modelID = "turncpu-model"

type options struct {
	evener      string
	turns       int
	rounds      int
	payloadKB   int
	streamBytes int
	outDir      string
	cpuProfile  bool
	serve       bool
	window      int
	delegates   int
	childRounds int
	extraArgs   string
	probes      int
}

func main() {
	var o options
	flag.StringVar(&o.evener, "evener", "", "path to the evener binary under test (required)")
	flag.IntVar(&o.turns, "turns", 1, "turns to run (more than one needs --serve)")
	flag.IntVar(&o.rounds, "rounds", 200, "tool rounds in each turn before it ends")
	flag.IntVar(&o.payloadKB, "payload-kb", 20, "size of each tool output in KiB")
	flag.IntVar(&o.streamBytes, "stream-bytes", 2000, "bytes of reasoning and of text streamed per round, in 4-byte deltas")
	flag.StringVar(&o.outDir, "out", "", "directory for logs and the profile (default: a new temp dir)")
	flag.BoolVar(&o.cpuProfile, "cpu-profile", false, "have evener write cpu.pprof into --out")
	flag.IntVar(&o.window, "context-window", 0, "context window in tokens to configure for the scripted model (0: the provider default); larger windows let history, and every request, grow larger before compaction")
	flag.IntVar(&o.delegates, "delegates", 0, "background delegates the root session spawns in its first rounds; each runs --child-rounds tool rounds of its own")
	flag.IntVar(&o.childRounds, "child-rounds", 40, "tool rounds each delegate runs before reporting back")
	flag.StringVar(&o.extraArgs, "evener-args", "", "extra space-separated flags passed to evener, e.g. --verbose")
	flag.IntVar(&o.probes, "probes", 0, "with --serve, after the turns finish probe the idle daemon this many times the way the hub does (fresh connection, initialize, thread/list with subagents, thread/read) and report CPU and bytes per probe")
	flag.BoolVar(&o.serve, "serve", false, "run evener serve and drive it over AppWire with a subscribed client")
	flag.Parse()
	if runtime.GOOS != "linux" {
		log.Fatalf("turncpu: reads CPU time from /proc, so it runs only on Linux (not %s)", runtime.GOOS)
	}
	if err := o.validate(); err != nil {
		fmt.Fprintf(os.Stderr, "turncpu: %v\n", err)
		flag.Usage()
		os.Exit(2)
	}
	if err := run(o); err != nil {
		log.Fatalf("turncpu: %v", err)
	}
}

func (o options) validate() error {
	switch {
	case o.evener == "":
		return errors.New("--evener is required")
	case o.turns < 1 || o.rounds < 1:
		return errors.New("--turns and --rounds must be at least 1")
	case o.turns > 1 && !o.serve:
		return errors.New("more than one turn needs --serve")
	case o.payloadKB < 0 || o.streamBytes < 0 || o.delegates < 0 || o.childRounds < 0 || o.window < 0 || o.probes < 0:
		return errors.New("--payload-kb, --stream-bytes, --delegates, --child-rounds, --context-window and --probes must not be negative")
	case o.delegates > o.rounds:
		// Delegates are spawned one per round of the first turn, and the last
		// turn runs until every one has reported: with more delegates than
		// rounds, a multi-turn run's first turn ends short of spawning them
		// all and its last turn never ends.
		return fmt.Errorf("--delegates (%d) must not exceed --rounds (%d)", o.delegates, o.rounds)
	}
	return nil
}

// provider is the scripted chat-completions endpoint. Each tool-bearing
// request is one session round; requests without tools (the session namer)
// get a canned JSON answer, as a non-streaming completion when not asked to
// stream.
type provider struct {
	o       options
	workDir string

	mu          sync.Mutex
	pid         int
	turn        int
	roundInTurn int
	lastReply   time.Duration // child cpu when the previous round's answer began
	perRound    [][]time.Duration
	messages    [][]int
	rootSession string         // the first session to make a tool-bearing request
	children    map[string]int // delegate session id -> rounds answered
	childDone   int
	allRounds   int
}

// toolRound is the scripted work for round i of any session.
func (p *provider) toolRound(i int) (name, args string) {
	file := fmt.Sprintf("f%02d.txt", i%files)
	if i%2 == 0 {
		return "read_file", fmt.Sprintf(`{"file_path":%q}`, filepath.Join(p.workDir, file))
	}
	return "shell", fmt.Sprintf(`{"command":"cat %s","description":"show %s"}`, file, file)
}

const endTurnArgs = `{"message":"done","end_turn":true,"output":{"message":"","data":{},"artifacts":[]}}`

const files = 16

func (p *provider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/models") {
		_, _ = fmt.Fprintf(w, `{"object":"list","data":[{"id":%q,"object":"model"}]}`, modelID)
		return
	}
	var body struct {
		Messages []json.RawMessage `json:"messages"`
		Tools    []json.RawMessage `json:"tools"`
		Stream   bool              `json:"stream"`
	}
	raw, err := io.ReadAll(r.Body)
	if err == nil {
		err = json.Unmarshal(raw, &body)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(body.Tools) == 0 {
		const name = `{"name":"CPU Harness"}`
		if !body.Stream {
			writeCompletion(w, name)
			return
		}
		writeStream(w, len(raw), name, "", 0, "", "")
		return
	}

	// The instance sends session affinity headers, so every request names
	// the session making it.
	session := r.Header.Get("session_id")
	p.mu.Lock()
	p.allRounds++
	if p.rootSession == "" {
		p.rootSession = session
	}
	if session != p.rootSession {
		i := p.children[session]
		p.children[session]++
		name, args := p.toolRound(i)
		if i >= p.o.childRounds {
			name, args = "communicate", endTurnArgs
			if i == p.o.childRounds {
				p.childDone++
			}
		}
		p.mu.Unlock()
		writeStream(w, len(raw), "", prose("thinking", i, p.o.streamBytes), p.o.streamBytes, name, args)
		return
	}
	if p.turn >= p.o.turns {
		// A wake after the last turn (a delegate reporting back late).
		p.mu.Unlock()
		writeStream(w, len(raw), "", "", 0, "communicate", endTurnArgs)
		return
	}
	if p.roundInTurn == 0 {
		p.perRound = append(p.perRound, nil)
		p.messages = append(p.messages, nil)
	}
	t, i := p.turn, p.roundInTurn
	if i > 0 { // a turn's first request follows the harness's idle wait, not a round
		p.perRound[t] = append(p.perRound[t], cpuTime(p.pid)-p.lastReply)
		p.messages[t] = append(p.messages[t], len(body.Messages))
	}
	// The last turn keeps working until every delegate has reported, so the
	// run measures the whole tree.
	endTurn := i >= p.o.rounds && (t < p.o.turns-1 || p.childDone >= p.o.delegates)
	if endTurn {
		p.turn++
		p.roundInTurn = 0
	} else {
		p.roundInTurn++
	}
	// Taken before answering, while the daemon waits on this response, so
	// the next round's request can never be measured against a stale value.
	p.lastReply = cpuTime(p.pid)
	p.mu.Unlock()

	name, args := p.toolRound(i)
	switch {
	case endTurn:
		name, args = "communicate", endTurnArgs
	case t == 0 && i < p.o.delegates:
		name, args = "delegate", fmt.Sprintf(`{"prompt":"CHILD-TASK-%d: work through the files"}`, i)
	}
	writeStream(w, len(raw), "", prose("thinking", i, p.o.streamBytes), p.o.streamBytes, name, args)
}

// writeCompletion answers a non-streaming request with text.
func writeCompletion(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "application/json")
	encoded, _ := json.Marshal(map[string]any{
		"id":      "turncpu",
		"model":   modelID,
		"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
	})
	_, _ = w.Write(encoded)
}

// writeStream answers one request as an SSE chat-completions stream: text
// (whole), then reasoning and prose in 4-byte deltas, then the tool call.
func writeStream(w http.ResponseWriter, requestBytes int, text, reasoning string, proseBytes int, toolName, toolArgs string) {
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	send := func(delta map[string]any, finish any, usage map[string]any) {
		chunk := map[string]any{"id": "turncpu", "model": modelID, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
		if usage != nil {
			chunk["usage"] = usage
		}
		encoded, _ := json.Marshal(chunk)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
	}
	if text != "" {
		send(map[string]any{"role": "assistant", "content": text}, nil, nil)
	}
	for part := range chunks(reasoning, 4) {
		send(map[string]any{"reasoning_content": part}, nil, nil)
	}
	if proseBytes > 0 {
		for part := range chunks(prose("working", 0, proseBytes), 4) {
			send(map[string]any{"content": part}, nil, nil)
		}
	}
	finish := "stop"
	if toolName != "" {
		finish = "tool_calls"
		send(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("call_%d", time.Now().UnixNano()), "type": "function", "function": map[string]any{"name": toolName, "arguments": ""}}}}, nil, nil)
		for part := range chunks(toolArgs, 8) {
			send(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": part}}}}, nil, nil)
		}
	}
	// Report a realistic prompt size so context management sees the history
	// it would against a real provider.
	promptTokens := requestBytes / 4
	send(map[string]any{}, finish, map[string]any{"prompt_tokens": promptTokens, "completion_tokens": 100, "total_tokens": promptTokens + 100})
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func chunks(s string, n int) func(func(string) bool) {
	return func(yield func(string) bool) {
		for len(s) > 0 {
			k := min(n, len(s))
			if !yield(s[:k]) {
				return
			}
			s = s[k:]
		}
	}
}

func prose(word string, seed, n int) string {
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		fmt.Fprintf(&b, "%s about step %d part %d. ", word, seed, i)
	}
	return b.String()[:n]
}

func run(o options) error {
	if o.outDir == "" {
		d, err := os.MkdirTemp("", "turncpu-")
		if err != nil {
			return err
		}
		o.outDir = d
	}
	// Every path below reaches the child through env vars that must be
	// absolute (the host temp bases refuse relative entries).
	outDir, err := filepath.Abs(o.outDir)
	if err != nil {
		return err
	}
	o.outDir = outDir
	home := filepath.Join(o.outDir, "home")
	workDir := filepath.Join(o.outDir, "work")
	configDir := filepath.Join(home, "config")
	tmpBase := filepath.Join(o.outDir, "tmp")
	for _, d := range []string{workDir, filepath.Join(configDir, "evener"), tmpBase} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	// The session's temp containers and its startup crashed-scratch sweep
	// stay inside outDir, never the host's /tmp shared with real sessions.
	if err := os.Chmod(tmpBase, 0o777|os.ModeSticky); err != nil {
		return err
	}
	for i := range files {
		if err := os.WriteFile(filepath.Join(workDir, fmt.Sprintf("f%02d.txt", i)), payload(i, o.payloadKB), 0o600); err != nil {
			return err
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()
	prov := &provider{o: o, workDir: workDir, children: map[string]int{}}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	httpServer := &http.Server{Handler: prov, ReadHeaderTimeout: 10 * time.Second}
	go httpServer.Serve(listener) //nolint:errcheck // closed on return
	defer httpServer.Close()      //nolint:errcheck // harness teardown
	providers := fmt.Sprintf("default = \"fake\"\n\n[providers.fake]\nbase = \"openai-compatible\"\nbase_url = %q\napi_key = \"turncpu-not-a-secret\"\nsession_affinity_headers = true\n", "http://"+listener.Addr().String()+"/v1")
	if o.window > 0 {
		providers += fmt.Sprintf("context_window = %d\n", o.window)
	}
	if err := os.WriteFile(filepath.Join(configDir, "evener", "providers.toml"), []byte(providers), 0o600); err != nil {
		return err
	}

	var args []string
	if o.serve {
		args = append(args, "serve", "--addr", "127.0.0.1:0")
	}
	args = append(args,
		"--model", "fake/"+modelID,
		"--dir", workDir,
		"--state-dir", filepath.Join(o.outDir, "state"),
		"--max-rounds", "0",
	)
	if o.cpuProfile {
		args = append(args, "--cpu-profile", filepath.Join(o.outDir, "cpu.pprof"))
	}
	args = append(args, strings.Fields(o.extraArgs)...)
	if !o.serve {
		args = append(args, "work through the files")
	}
	cmd := exec.CommandContext(ctx, o.evener, args...)
	cmd.Env = childEnv(os.Environ(), home, configDir, tmpBase)
	logPath := filepath.Join(o.outDir, "evener.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer logFile.Close() //nolint:errcheck // harness log
	cmd.Stdout = logFile
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	// Held across Start so a request the child makes before its pid is
	// recorded waits for it instead of being measured against pid 0.
	prov.mu.Lock()
	if err := cmd.Start(); err != nil {
		prov.mu.Unlock()
		return err
	}
	prov.pid = cmd.Process.Pid
	prov.mu.Unlock()
	addrCh := make(chan string, 1)
	teeDone := make(chan struct{})
	go func() {
		defer close(teeDone)
		teeStderr(stderr, logFile, addrCh)
	}()

	exited := make(chan error, 1)
	go func() {
		<-teeDone // Wait closes the pipe; drain it first
		exited <- cmd.Wait()
	}()

	start := time.Now()
	var notifications int
	if o.serve {
		var addr string
		select {
		case addr = <-addrCh:
		case err := <-exited:
			return fmt.Errorf("evener serve exited before listening: %w (see %s)", err, logPath)
		}
		n, err := driveServe(ctx, addr, o.turns, exited)
		if err != nil {
			return fmt.Errorf("%w (see %s)", err, logPath)
		}
		notifications = n
		if o.probes > 0 {
			if err := probeIdle(ctx, addr, o.probes, cmd.Process.Pid); err != nil {
				return fmt.Errorf("%w (see %s)", err, logPath)
			}
		}
		// SIGINT lets serve stop its profile and exit cleanly.
		_ = cmd.Process.Signal(syscall.SIGINT)
	}
	select {
	case err := <-exited:
		if err != nil {
			log.Printf("evener exited: %v (see %s)", err, logPath)
		}
	case <-ctx.Done():
		return fmt.Errorf("timed out (see %s)", logPath)
	}
	prov.mu.Lock()
	defer prov.mu.Unlock()
	var processCPU time.Duration
	if cmd.ProcessState != nil {
		processCPU = cmd.ProcessState.UserTime() + cmd.ProcessState.SystemTime()
	}
	fmt.Printf("out: %s\nwall: %s  process cpu: %s  notifications: %d  model rounds (all sessions): %d  delegates reported: %d/%d\n",
		o.outDir, time.Since(start).Round(time.Millisecond), processCPU.Round(time.Millisecond), notifications, prov.allRounds, prov.childDone, o.delegates)
	report(prov.perRound, prov.messages)
	if prov.turn < o.turns {
		return fmt.Errorf("session stopped after %d of %d turns (see %s)", prov.turn, o.turns, logPath)
	}
	return nil
}

// childEnv is inherited with every EVENER_ setting dropped (a developer's
// providers.toml path, model or state dir would point the run away from the
// scripted provider and --out), HOME, XDG dirs and temp bases moved under
// --out, and the registry kept offline so no models.dev refresh adds network
// work to the measurement.
func childEnv(inherited []string, home, configDir, tmpBase string) []string {
	env := slices.DeleteFunc(slices.Clone(inherited), func(kv string) bool {
		return strings.HasPrefix(kv, "EVENER_")
	})
	return append(env,
		"HOME="+home,
		"XDG_CONFIG_HOME="+configDir,
		"XDG_STATE_HOME="+filepath.Join(home, "state"),
		"XDG_CACHE_HOME="+filepath.Join(home, "cache"),
		"TMPDIR="+tmpBase,
		envvars.EVENERHostTempBases.Assignment(tmpBase),
		envvars.EVENEROffline.Assignment("1"),
	)
}

var listeningRE = regexp.MustCompile(`listening on (\S+)`)

func teeStderr(r io.Reader, w io.Writer, addrCh chan<- string) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	sent := false
	for sc.Scan() {
		line := sc.Text()
		_, _ = fmt.Fprintln(w, line)
		if m := listeningRE.FindStringSubmatch(line); m != nil && !sent {
			addrCh <- m[1]
			sent = true
		}
	}
}

// driveServe subscribes to the daemon's thread over AppWire and runs turns
// one after another, starting each once the previous one has completed.
func driveServe(ctx context.Context, addr string, turns int, exited <-chan error) (int, error) {
	transport, err := appwire.DialWebSocket(ctx, "ws://"+addr+"/rpc", http.DefaultClient)
	if err != nil {
		return 0, fmt.Errorf("dial: %w", err)
	}
	defer transport.Close() //nolint:errcheck // harness teardown
	client := appwire.NewClient(transport)
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion, ClientInfo: appwire.ClientInfo{Name: "turncpu"}}); err != nil {
		return 0, fmt.Errorf("initialize: %w", err)
	}
	read, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Subscribe: true})
	if err != nil {
		return 0, fmt.Errorf("thread read: %w", err)
	}
	count := 0
	for turn := range turns {
		started, err := client.TurnStart(ctx, appwire.TurnStartParams{
			ClientMutationID:   fmt.Sprintf("turncpu-%d", turn),
			ThreadID:           read.Thread.ID,
			ExpectedInstanceID: read.Thread.ID,
			Input:              []appwire.InputItem{{Type: "text", Text: fmt.Sprintf("turn %d: work through the files", turn)}},
		})
		if err != nil {
			return count, fmt.Errorf("turn %d start: %w", turn, err)
		}
		// The thread leaves "active" only after the turn's turn/completed;
		// the next turn/start is refused until it does.
		for completed, settled := false, false; !completed || !settled; {
			select {
			case n, ok := <-client.Notifications():
				if !ok {
					return count, errors.New("notification stream closed")
				}
				count++
				switch n.Method {
				case appwire.NotifyTurnCompleted:
					var params appwire.TurnCompletedParams
					completed = completed || (json.Unmarshal(n.Params, &params) == nil && params.Turn.ID == started.Turn.ID)
				case appwire.NotifyThreadStatusChanged:
					var params appwire.ThreadStatusChangedParams
					settled = completed && json.Unmarshal(n.Params, &params) == nil && params.Status.Type != appwire.ThreadStatusActive
				}
			case err := <-exited:
				return count, fmt.Errorf("evener serve exited mid-turn: %w", err)
			case <-ctx.Done():
				return count, ctx.Err()
			}
		}
	}
	return count, nil
}

// probeIdle repeats the hub's status probe against the idle daemon and
// reports the daemon CPU and response bytes each probe costs.
func probeIdle(ctx context.Context, addr string, probes, pid int) error {
	var bytes int
	start := cpuTime(pid)
	for range probes {
		transport, err := appwire.DialWebSocket(ctx, "ws://"+addr+"/rpc", http.DefaultClient)
		if err != nil {
			return fmt.Errorf("probe dial: %w", err)
		}
		client := appwire.NewClient(transport)
		client.Start(ctx)
		if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion, ClientInfo: appwire.ClientInfo{Name: "turncpu-probe"}}); err != nil {
			return fmt.Errorf("probe initialize: %w", err)
		}
		list, err := client.ThreadList(ctx, appwire.ThreadListParams{IncludeSubagents: true})
		if err != nil {
			return fmt.Errorf("probe thread/list: %w", err)
		}
		read, err := client.ThreadRead(ctx, appwire.ThreadReadParams{})
		if err != nil {
			return fmt.Errorf("probe thread/read: %w", err)
		}
		for _, v := range []any{list, read} {
			encoded, _ := json.Marshal(v)
			bytes += len(encoded)
		}
		_ = transport.Close()
	}
	spent := cpuTime(pid) - start
	fmt.Printf("probes: %d  daemon cpu/probe %.1fms  response bytes/probe %d\n", probes, float64(spent.Microseconds())/1000/float64(probes), bytes/probes)
	return nil
}

// report prints mean per-round CPU. A single turn is split into buckets of
// rounds; several turns get one line each, so growth with the session's
// total history is visible at a glance.
func report(perRound [][]time.Duration, msgs [][]int) {
	var total time.Duration
	line := func(label string, rounds []time.Duration, lastMsgs int) {
		var sum time.Duration
		for _, d := range rounds {
			sum += d
		}
		total += sum
		fmt.Printf("%s: mean cpu/round %6.1fms  messages=%d\n", label, float64(sum.Microseconds())/1000/float64(max(len(rounds), 1)), lastMsgs)
	}
	if len(perRound) == 1 {
		const bucket = 25
		rounds, m := perRound[0], msgs[0]
		for i := 0; i < len(rounds); i += bucket {
			end := min(i+bucket, len(rounds))
			line(fmt.Sprintf("rounds %3d-%3d", i+1, end), rounds[i:end], m[end-1])
		}
	} else {
		for t, rounds := range perRound {
			last := 0
			if len(msgs[t]) > 0 {
				last = msgs[t][len(msgs[t])-1]
			}
			line(fmt.Sprintf("turn %3d", t+1), rounds, last)
		}
	}
	fmt.Printf("total cpu across rounds: %s\n", total.Round(time.Millisecond))
}

// cpuTime is the process's user+system CPU from /proc/<pid>/stat, in clock
// ticks converted at the Linux-standard 100Hz.
func cpuTime(pid int) time.Duration {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	s := string(raw)
	end := strings.LastIndexByte(s, ')')
	if end < 0 {
		return 0
	}
	fields := strings.Fields(s[end+1:])
	if len(fields) < 13 {
		return 0
	}
	ut, _ := strconv.ParseInt(fields[11], 10, 64)
	st, _ := strconv.ParseInt(fields[12], 10, 64)
	return time.Duration(ut+st) * 10 * time.Millisecond
}

func payload(seed, kb int) []byte {
	var b strings.Builder
	for line := 0; b.Len() < kb*1024; line++ {
		fmt.Fprintf(&b, "file %d line %d: the quick brown fox jumps over the lazy dog %d\n", seed, line, line*seed)
	}
	return []byte(b.String())
}
