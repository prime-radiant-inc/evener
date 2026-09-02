package agent

import (
	"context"
	"strings"
	"testing"

	taskpkg "primeradiant.com/evener/agent/task"
)

// The delegate brief parameter is `prompt`; `task_list` seeds the delegate's
// task list. Both decode from the raw tool args.
func TestDecodeDelegateArgs_PromptAndTaskList(t *testing.T) {
	a, err := decodeDelegateArgs(map[string]any{
		"prompt": "do work",
		"task_list": []any{
			map[string]any{"title": "inspect", "prompt": "read the spec"},
			map[string]any{"title": "verify", "prompt": "run the check", "reasoning_effort": "low", "type": "verify"},
		},
	})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if a.Task != "do work" {
		t.Fatalf("prompt decoded as %q", a.Task)
	}
	want := []taskpkg.TaskTemplate{
		{Title: "inspect", Prompt: "read the spec"},
		{Title: "verify", Prompt: "run the check", ReasoningEffort: "low", Type: "verify"},
	}
	if len(a.TaskList) != len(want) {
		t.Fatalf("task_list = %+v, want %+v", a.TaskList, want)
	}
	for i := range want {
		if a.TaskList[i] != want[i] {
			t.Errorf("task_list[%d] = %+v, want %+v", i, a.TaskList[i], want[i])
		}
	}
}

func TestDecodeDelegateArgs_TaskListRejectsMalformed(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"not an array", map[string]any{"prompt": "p", "task_list": "step one"}, "task_list must be an array"},
		{"item not an object", map[string]any{"prompt": "p", "task_list": []any{"step one"}}, "task_list[0] must be an object"},
		{"missing prompt", map[string]any{"prompt": "p", "task_list": []any{map[string]any{"title": "inspect"}}}, "task_list[0].prompt is required"},
		{"missing title", map[string]any{"prompt": "p", "task_list": []any{map[string]any{"prompt": "read"}}}, "task_list[0].title is required"},
		{"bad reasoning_effort", map[string]any{"prompt": "p", "task_list": []any{map[string]any{"title": "t", "prompt": "read", "reasoning_effort": "xhigh"}}}, "task_list[0].reasoning_effort must be one of low, medium, high"},
		{"bad type", map[string]any{"prompt": "p", "task_list": []any{map[string]any{"title": "t", "prompt": "read", "type": "bogus"}}}, "task_list[0].type must be one of research, implement, verify, fix"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeDelegateArgs(tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// The parent's task_list is frozen into the delegate descriptor at creation,
// so launch and every later resume seed the child's task list from it.
func TestCreateDelegate_TaskListFreezesIntoDescriptor(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	parent := []taskpkg.TaskTemplate{
		{Title: "inspect", Prompt: "read the spec"},
		{Title: "verify", Prompt: "run the check", Type: "verify"},
	}
	result := root.createDelegate(context.Background(), delegateArgs{Task: "brief", TaskList: parent})
	if result.Err != nil {
		t.Fatalf("createDelegate: %v", result.Err)
	}
	root.delegateController.mu.Lock()
	aggregate := root.delegateController.durable[result.DelegateID]
	live := root.delegateController.live[result.DelegateID]
	root.delegateController.mu.Unlock()
	if aggregate == nil {
		t.Fatal("no durable aggregate for the created delegate")
	}
	got := aggregate.Descriptor.TaskTemplates
	if len(got) != 2 || got[0].Title != "inspect" || got[1].Title != "verify" || got[1].Type != "verify" {
		t.Fatalf("descriptor task templates = %+v, want the parent's two tasks", got)
	}
	if live == nil || live.binding == nil || live.binding.runtime == nil {
		t.Fatal("no live child session for the created delegate")
	}
	tasks := live.binding.runtime.getOrCreateTaskStore().View()
	if len(tasks) != 2 || tasks[0].Description != "inspect" || tasks[1].Description != "verify" {
		t.Fatalf("child task list = %+v, want the parent's two tasks", tasks)
	}
	if tasks[0].Status != taskpkg.TaskInProgress || tasks[1].Status != taskpkg.TaskOpen {
		t.Errorf("child task statuses = %q,%q, want in_progress then open", tasks[0].Status, tasks[1].Status)
	}
}

// An empty brief is an error at decode time: a delegate started without input
// sits idle, which hangs whoever waits on it.
func TestDecodeDelegateArgs_RejectsEmptyPrompt(t *testing.T) {
	for _, args := range []map[string]any{{}, {"prompt": "   "}, {"task": "old key"}} {
		_, err := decodeDelegateArgs(args)
		if err == nil || !strings.Contains(err.Error(), "invalid_request: prompt is required") {
			t.Errorf("decode(%v) err = %v, want prompt-required invalid_request", args, err)
		}
	}
}

// The subagent role carries the delegate and job-control tools so that the
// parent's delegation_allowance decides whether it may delegate: granted, the
// child registers delegate; not granted, it stays a leaf.
func TestBuiltinAgents_SubagentCarriesDelegationTools(t *testing.T) {
	agents, err := builtinAgents()
	if err != nil {
		t.Fatalf("builtinAgents: %v", err)
	}
	have := map[string]bool{}
	for _, tool := range agents["subagent"].Tools {
		have[tool] = true
	}
	for _, want := range []string{"delegate", "delegate_send", "job_status", "job_watch", "job_stop", "job_list", "read_transcript"} {
		if !have[want] {
			t.Errorf("subagent role missing tool %q", want)
		}
	}
}

func TestCreateDelegate_SubagentDelegatesOnlyWhenGranted(t *testing.T) {
	for _, tc := range []struct {
		name      string
		allowance int
		want      bool
	}{{"granted", 1, true}, {"leaf", 0, false}} {
		t.Run(tc.name, func(t *testing.T) {
			root, _, _ := newDelegateResourceBootstrapSession(t)
			result := root.createDelegate(context.Background(), delegateArgs{Task: "brief", AgentType: "subagent", DelegationAllowance: tc.allowance})
			if result.Err != nil {
				t.Fatalf("createDelegate: %v", result.Err)
			}
			root.delegateController.mu.Lock()
			live := root.delegateController.live[result.DelegateID]
			root.delegateController.mu.Unlock()
			if live == nil || live.binding == nil || live.binding.runtime == nil {
				t.Fatal("no live child session")
			}
			names := live.binding.runtime.reg.RegisteredNames()
			if names["delegate"] != tc.want {
				t.Errorf("child has delegate tool = %v, want %v (allowance %d)", names["delegate"], tc.want, tc.allowance)
			}
			if tc.want && !names["job_watch"] {
				t.Error("granted subagent should also have job_watch to supervise its own delegates")
			}
		})
	}
}
