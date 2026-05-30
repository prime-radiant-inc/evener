package llm

import (
	"context"
	"testing"
)

type stubLister struct {
	stubAdapter
	models []ModelInfo
	err    error
}

func (s *stubLister) ListModels(ctx context.Context) ([]ModelInfo, error) {
	return s.models, s.err
}

type stubAdapter struct{}

func (s *stubAdapter) Name() string { return "stub" }
func (s *stubAdapter) Complete(ctx context.Context, req Request) (Response, error) {
	return Response{}, nil
}
func (s *stubAdapter) Stream(ctx context.Context, req Request) (Stream, error) { return nil, nil }

func TestClient_ListModels_Delegates(t *testing.T) {
	c := NewClient()
	c.Register(&stubLister{
		models: []ModelInfo{{ID: "m1", Provider: "stub"}, {ID: "m2", Provider: "stub"}},
	})

	models, err := c.ListModels(context.Background(), "stub")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2", len(models))
	}
	if models[0].ID != "m1" {
		t.Fatalf("models[0].ID = %q, want m1", models[0].ID)
	}
}

func TestClient_ListModels_NotImplemented(t *testing.T) {
	c := NewClient()
	c.Register(&stubAdapter{})

	_, err := c.ListModels(context.Background(), "stub")
	if err == nil {
		t.Fatal("expected error for adapter that doesn't implement ModelLister")
	}
}

func TestClient_ListModels_UnknownProvider(t *testing.T) {
	c := NewClient()
	_, err := c.ListModels(context.Background(), "nope")
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}
