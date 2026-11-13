package responses

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/internal/transporttest"
)

// rejectingUserReq builds a request whose body cannot be built: an audio part
// in the user message is a content kind the Responses wire format rejects.
func rejectingUserReq() llm.Request {
	req := userReq("hi")
	req.Messages[1].Content = append(req.Messages[1].Content, llm.ContentPart{
		Kind:  llm.ContentAudio,
		Audio: &llm.AudioData{URL: "https://example.invalid/a.wav"},
	})
	return req
}

// TestEntrypointsPropagateBuildErrors pins that Complete, Stream and
// CountTokens return the structured build error before any HTTP exchange when
// the request cannot be shaped at all — each entrypoint has its own early
// return the wire plumbing can hide behind.
func TestEntrypointsPropagateBuildErrors(t *testing.T) {
	srv := transporttest.NoHTTPServer(t)
	res := liveRes(srv, openaiCaps)
	p := &Protocol{Client: srv.Client()}

	if _, err := p.Complete(context.Background(), rejectingUserReq(), res); err == nil {
		t.Fatal("Complete must fail on an unshapeable request")
	} else if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok {
		t.Fatalf("Complete err = %v, want the structured configuration error", err)
	}

	if _, err := p.Stream(context.Background(), rejectingUserReq(), res); err == nil {
		t.Fatal("Stream must fail on an unshapeable request")
	} else if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok {
		t.Fatalf("Stream err = %v, want the structured configuration error", err)
	}

	if _, err := p.CountTokens(context.Background(), rejectingUserReq(), res); err == nil {
		t.Fatal("CountTokens must fail on an unshapeable request")
	} else if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok {
		t.Fatalf("CountTokens err = %v, want the structured configuration error", err)
	}
}

// TestStampResponseIDHashOnRawlessResponse pins the defensive arm of
// stampResponseIDHash: a response whose Raw map is still nil gets one so the
// id_hash can be recorded.
func TestStampResponseIDHashOnRawlessResponse(t *testing.T) {
	hasher := llm.NewContinuationHasher([]byte("fuzz-secret"))
	want, err := hasher.HashContinuationHandle("response_id", "resp_x")
	if err != nil {
		t.Fatalf("HashContinuationHandle: %v", err)
	}
	p := &Protocol{Hasher: hasher}
	resp := llm.Response{ID: "resp_x"} // Raw intentionally nil
	p.stampResponseIDHash(context.Background(), &resp)
	if resp.Raw == nil {
		t.Fatal("a nil Raw map must be initialized before stamping")
	}
	if resp.Raw["id_hash"] != want {
		t.Fatalf("id_hash = %v, want %q", resp.Raw["id_hash"], want)
	}
}

// TestListModelsCodexRowShapes pins the Codex listing edges: a row with no
// identifier under any of its three keys is skipped, a row with an id but no
// reasoning levels advertises none, and the surviving rows come back sorted.
func TestListModelsCodexRowShapes(t *testing.T) {
	srv, _ := server(t, 200, `{"models":[{"context_window":1000},{"slug":"gpt-b","supported_reasoning_levels":[]},{"slug":"gpt-a","supported_reasoning_levels":[{"effort":"low"}]}]}`)
	rows, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), liveRes(srv, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != "gpt-a" || rows[1].ID != "gpt-b" {
		t.Fatalf("rows = %+v, want the two identifiable rows sorted", rows)
	}
	if len(rows[0].Caps.EffortValues) != 1 || rows[0].Caps.EffortValues[0] != "low" {
		t.Fatalf("gpt-a efforts = %v", rows[0].Caps.EffortValues)
	}
	if rows[1].Caps.EffortValues != nil || rows[1].Caps.Reasoning != nil {
		t.Fatalf("a row with no reasoning levels must advertise none: %+v", rows[1].Caps)
	}
}

// TestListModelsDecodeFailure pins the listing's decode arm: a 200 body that
// is not the models JSON shape fails the listing rather than yielding empty
// rows silently.
func TestListModelsDecodeFailure(t *testing.T) {
	srv, _ := server(t, 200, `not json at all`)
	_, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), liveRes(srv, nil))
	if err == nil {
		t.Fatal("an undecodable models listing must fail")
	}
}
