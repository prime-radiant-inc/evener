package server

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
)

func TestSubmitNotification_PushesEntryNotification(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SubmitNotification()
	select {
	case msg := <-srv.InputCh():
		if msg.Kind != agent.EntryNotification {
			t.Errorf("Kind: got %v, want EntryNotification", msg.Kind)
		}
		if msg.Text != "" {
			t.Errorf("Text: got %q, want empty", msg.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout: SubmitNotification did not deliver a message")
	}
}

func TestSubmitNotification_DropIfFull(t *testing.T) {
	srv := NewServer(ServerConfig{})
	// Fill the 1-slot buffer.
	srv.SubmitNotification()
	// Second call must not block even though the channel is full.
	done := make(chan struct{})
	go func() {
		srv.SubmitNotification()
		close(done)
	}()
	select {
	case <-done:
		// expected: returned without blocking
	case <-time.After(time.Second):
		t.Fatal("SubmitNotification blocked on full channel")
	}
}

func TestSubmitContinuation(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SubmitContinuation("continue")
	select {
	case msg := <-srv.InputCh():
		if msg.Kind != agent.EntryContinuation {
			t.Errorf("Kind: got %v, want EntryContinuation", msg.Kind)
		}
		if msg.Text != "continue" {
			t.Errorf("Text: got %q, want continue", msg.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout: SubmitContinuation did not deliver a message")
	}
}

func TestSubmitContinuation_DropIfFull(t *testing.T) {
	srv := NewServer(ServerConfig{})
	// Fill the 1-slot buffer.
	srv.SubmitContinuation("first")
	// Second call must not block even though the channel is full.
	done := make(chan struct{})
	go func() {
		srv.SubmitContinuation("second")
		close(done)
	}()
	select {
	case <-done:
		// expected: returned without blocking
	case <-time.After(time.Second):
		t.Fatal("SubmitContinuation blocked on full channel")
	}
}

func TestServerAppWireThreadList(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")

	conn := srv.AppServer().NewConnection("test")
	init := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if init.Kind() != appwire.MessageResponse {
		t.Fatalf("init=%v", init.Kind())
	}
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadList, appwire.ThreadListParams{}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	out, ok := resp.Response.Result.(appwire.ThreadListResponse)
	if !ok {
		t.Fatalf("thread/list result=%T (%+v)", resp.Response.Result, resp)
	}
	if len(out.Data) != 1 {
		t.Fatalf("thread/list data: got %d threads, want 1", len(out.Data))
	}
	thread := out.Data[0]
	if thread.ID != "th_1" {
		t.Errorf("thread ID: got %q, want th_1", thread.ID)
	}
	if thread.Source != "local" {
		t.Errorf("thread Source: got %q, want local", thread.Source)
	}
	if thread.Evener.Ref != "local:th_1" {
		t.Errorf("thread Evener.Ref: got %q, want local:th_1", thread.Evener.Ref)
	}
}

func TestServerAppWireTasksList(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetTasksFunc(func() any {
		return []map[string]any{{"id": "1"}}
	})

	conn := srv.AppServer().NewConnection("test")
	init := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if init.Kind() != appwire.MessageResponse {
		t.Fatalf("init=%v", init.Kind())
	}
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodEvenerTasksList, appwire.TaskListParams{}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	out, ok := resp.Response.Result.(appwire.TaskListResponse)
	if !ok {
		t.Fatalf("evener/tasks/list result=%T (%+v)", resp.Response.Result, resp)
	}
	tasks, ok := out.Data.([]map[string]any)
	if !ok {
		t.Fatalf("task data type=%T, want []map[string]any", out.Data)
	}
	if len(tasks) != 1 {
		t.Fatalf("task data: got %d tasks, want 1", len(tasks))
	}
	if tasks[0]["id"] != "1" {
		t.Errorf("task id: got %v, want 1", tasks[0]["id"])
	}
}

func TestHandleAppJobsListNilFunc(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")

	conn := srv.AppServer().NewConnection("test")
	init := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if init.Kind() != appwire.MessageResponse {
		t.Fatalf("init=%v", init.Kind())
	}
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodEvenerJobsList, appwire.JobsListParams{}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v (%+v)", resp.Kind(), resp.Error)
	}
	out, ok := resp.Response.Result.(appwire.JobsListResponse)
	if !ok {
		t.Fatalf("evener/jobs/list result=%T (%+v)", resp.Response.Result, resp)
	}
	if out.Data != nil {
		t.Errorf("data: got %+v, want nil", out.Data)
	}
}

func TestHandleAppJobsList(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetJobsFunc(func(got appwire.JobsListParams) (any, error) {
		if got.Ref != "local:root" || got.Continuation != "next" {
			t.Fatalf("params=%+v", got)
		}
		return appwire.JobActivityTree{Root: appwire.JobActivitySession{SessionID: "root", Ref: "local:root"}}, nil
	})

	conn := srv.AppServer().NewConnection("test")
	init := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if init.Kind() != appwire.MessageResponse {
		t.Fatalf("init=%v", init.Kind())
	}
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodEvenerJobsList, appwire.JobsListParams{Ref: "local:root", Continuation: "next"}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v (%+v)", resp.Kind(), resp.Error)
	}
	out, ok := resp.Response.Result.(appwire.JobsListResponse)
	if !ok {
		t.Fatalf("evener/jobs/list result=%T (%+v)", resp.Response.Result, resp)
	}
	tree, ok := out.Data.(appwire.JobActivityTree)
	if !ok {
		t.Fatalf("jobs data type=%T, want appwire.JobActivityTree", out.Data)
	}
	if tree.Root.SessionID != "root" || tree.Root.Ref != "local:root" {
		t.Fatalf("tree=%+v", tree)
	}
}

// A jobs source that cannot read its store answers the wire with a failure,
// not with the empty list a job-less session answers: "no jobs ran" and "I
// can't tell you what ran" must not arrive as the same response.
func TestHandleAppJobsListSourceError(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetJobsFunc(func(appwire.JobsListParams) (any, error) {
		return nil, errors.New("jobstore: parse event line 3: unexpected end of JSON input")
	})

	conn := srv.AppServer().NewConnection("test")
	init := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if init.Kind() != appwire.MessageResponse {
		t.Fatalf("init=%v", init.Kind())
	}
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodEvenerJobsList, appwire.JobsListParams{}))
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("resp=%v (%+v), want an error response", resp.Kind(), resp.Response)
	}
	if !strings.Contains(resp.Error.Error.Message, "parse event line 3") {
		t.Errorf("error message: %+v", resp.Error.Error)
	}
}

func TestHandleAppJobsOutputNilFunc(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")

	conn := srv.AppServer().NewConnection("test")
	init := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if init.Kind() != appwire.MessageResponse {
		t.Fatalf("init=%v", init.Kind())
	}
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodEvenerJobsOutput, appwire.JobsOutputParams{JobID: "job_1"}))
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("resp=%v, want error", resp.Kind())
	}
	if resp.Error.Error.Code != appwire.CodeUnavailable {
		t.Errorf("error code: got %d, want %d", resp.Error.Error.Code, appwire.CodeUnavailable)
	}
	data, ok := resp.Error.Error.Data.(appwire.ErrorData)
	if !ok {
		t.Fatalf("error data type=%T, want appwire.ErrorData", resp.Error.Error.Data)
	}
	if data.EvenerErrorInfo != appwire.ErrorActionUnavailable {
		t.Errorf("evenerErrorInfo: got %q, want actionUnavailable", data.EvenerErrorInfo)
	}
}

func TestHandleAppJobsOutputNotFound(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetJobOutputFunc(func(string, int64, int64) (any, bool, error) { return nil, false, nil })

	conn := srv.AppServer().NewConnection("test")
	init := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if init.Kind() != appwire.MessageResponse {
		t.Fatalf("init=%v", init.Kind())
	}
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodEvenerJobsOutput, appwire.JobsOutputParams{JobID: "job_missing"}))
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("resp=%v, want error", resp.Kind())
	}
	if resp.Error.Error.Code != appwire.CodeInvalidParams {
		t.Errorf("error code: got %d, want %d", resp.Error.Error.Code, appwire.CodeInvalidParams)
	}
	if !strings.Contains(resp.Error.Error.Message, "job_missing") {
		t.Errorf("error message %q does not carry the job id", resp.Error.Error.Message)
	}
}

func TestHandleAppJobsOutput(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetJobOutputFunc(func(jobID string, beforeBytes, maxBytes int64) (any, bool, error) {
		if jobID != "job_1" {
			t.Errorf("jobID = %q, want job_1", jobID)
		}
		if beforeBytes != 7 {
			t.Errorf("beforeBytes = %d, want 7", beforeBytes)
		}
		if maxBytes != 99 {
			t.Errorf("maxBytes = %d, want 99", maxBytes)
		}
		return agent.JobOutputTail{Tail: "hi", TotalBytes: 2}, true, nil
	})

	conn := srv.AppServer().NewConnection("test")
	init := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if init.Kind() != appwire.MessageResponse {
		t.Fatalf("init=%v", init.Kind())
	}
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodEvenerJobsOutput, appwire.JobsOutputParams{JobID: "job_1", BeforeBytes: 7, MaxBytes: 99}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v (%+v)", resp.Kind(), resp.Error)
	}
	out, ok := resp.Response.Result.(appwire.JobsOutputResponse)
	if !ok {
		t.Fatalf("evener/jobs/output result=%T (%+v)", resp.Response.Result, resp)
	}
	tail, ok := out.Data.(agent.JobOutputTail)
	if !ok {
		t.Fatalf("output data type=%T, want agent.JobOutputTail", out.Data)
	}
	if tail.Tail != "hi" {
		t.Errorf("tail: got %q, want hi", tail.Tail)
	}
}

func TestServerAppWireModelList(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetListModelsFunc(func(ctx context.Context) ([]appwire.ModelDescriptor, error) {
		return []appwire.ModelDescriptor{{Model: "gpt-4o"}}, nil
	})

	conn := srv.AppServer().NewConnection("test")
	init := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if init.Kind() != appwire.MessageResponse {
		t.Fatalf("init=%v", init.Kind())
	}
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodModelList, appwire.ModelListParams{}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	out, ok := resp.Response.Result.(appwire.ModelListResponse)
	if !ok {
		t.Fatalf("model/list result=%T (%+v)", resp.Response.Result, resp)
	}
	if len(out.Data) != 1 {
		t.Fatalf("model/list data: got %d models, want 1", len(out.Data))
	}
	if out.Data[0].Model != "gpt-4o" {
		t.Errorf("model: got %q, want gpt-4o", out.Data[0].Model)
	}
	if out.Data[0].Provider != "" {
		t.Errorf("provider: got %q, want empty (no profile set)", out.Data[0].Provider)
	}
}

func TestServerAppWireModelListEmptyDataIsArray(t *testing.T) {
	srv := NewServer(ServerConfig{})
	conn := srv.AppServer().NewConnection("test")
	init := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if init.Kind() != appwire.MessageResponse {
		t.Fatalf("init=%v", init.Kind())
	}
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodModelList, appwire.ModelListParams{}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	out, ok := resp.Response.Result.(appwire.ModelListResponse)
	if !ok {
		t.Fatalf("model/list result=%T (%+v)", resp.Response.Result, resp)
	}
	if out.Data == nil {
		t.Fatal("model/list data = nil, want an empty JSON array")
	}
}
