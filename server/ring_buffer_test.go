package server

import "testing"

func TestRingBuffer_AddAndRead(t *testing.T) {
	rb := NewRingBuffer(3)
	rb.Add("a")
	rb.Add("b")
	rb.Add("c")

	items := rb.After(0) // all items
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	if items[0].Value != "a" || items[0].ID != 1 {
		t.Errorf("item 0: got %+v", items[0])
	}
	if items[2].Value != "c" || items[2].ID != 3 {
		t.Errorf("item 2: got %+v", items[2])
	}
}

func TestRingBuffer_Overflow(t *testing.T) {
	rb := NewRingBuffer(3)
	for i := 0; i < 5; i++ {
		rb.Add(i)
	}
	items := rb.After(0) // only last 3 should remain
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	if items[0].Value != 2 || items[0].ID != 3 {
		t.Errorf("oldest item: got %+v", items[0])
	}
}

func TestRingBuffer_AfterID(t *testing.T) {
	rb := NewRingBuffer(10)
	rb.Add("a")
	rb.Add("b")
	rb.Add("c")
	rb.Add("d")

	items := rb.After(2) // after ID 2 → c, d
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Value != "c" {
		t.Errorf("first item: got %+v", items[0])
	}
}

func TestRingBuffer_AfterID_Evicted(t *testing.T) {
	rb := NewRingBuffer(3)
	for i := 0; i < 10; i++ {
		rb.Add(i)
	}
	// ID 2 has been evicted; should return all available
	items := rb.After(2)
	if len(items) != 3 {
		t.Fatalf("expected 3 items (all available), got %d", len(items))
	}
}

func TestRingBuffer_Empty(t *testing.T) {
	rb := NewRingBuffer(5)
	items := rb.After(0)
	if len(items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(items))
	}
}

func TestRingBuffer_ConcurrentSafe(t *testing.T) {
	rb := NewRingBuffer(100)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			rb.Add(i)
		}
		close(done)
	}()
	for {
		select {
		case <-done:
			return
		default:
			_ = rb.After(0)
		}
	}
}
