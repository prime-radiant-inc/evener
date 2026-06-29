package clock

import (
	"testing"
	"time"
)

func TestRealNowAdvances(t *testing.T) {
	c := Real()
	t0 := c.Now()
	c.Sleep(time.Millisecond)
	if !c.Now().After(t0) {
		t.Fatalf("Now did not advance across a Sleep")
	}
}

func TestRealAfterFires(t *testing.T) {
	c := Real()
	select {
	case <-c.After(5 * time.Millisecond):
	case <-time.After(time.Second):
		t.Fatal("After never fired")
	}
}

func TestRealNewTimerFiresAndStops(t *testing.T) {
	c := Real()
	tm := c.NewTimer(5 * time.Millisecond)
	select {
	case <-tm.C():
	case <-time.After(time.Second):
		t.Fatal("timer never fired")
	}

	tm2 := c.NewTimer(time.Hour)
	if !tm2.Stop() {
		t.Fatal("Stop on a pending timer reported it already fired")
	}
}

func TestRealNewTimerReset(t *testing.T) {
	c := Real()
	tm := c.NewTimer(time.Hour)
	if !tm.Reset(5 * time.Millisecond) {
		t.Fatal("Reset on a pending timer reported it already fired")
	}
	select {
	case <-tm.C():
	case <-time.After(time.Second):
		t.Fatal("timer never fired after Reset")
	}
}

func TestRealNewTicker(t *testing.T) {
	c := Real()
	tk := c.NewTicker(5 * time.Millisecond)
	defer tk.Stop()
	for i := 0; i < 2; i++ {
		select {
		case <-tk.C():
		case <-time.After(time.Second):
			t.Fatalf("ticker tick %d never fired", i)
		}
	}
}

func TestRealAfterFunc(t *testing.T) {
	c := Real()
	done := make(chan struct{})
	c.AfterFunc(5*time.Millisecond, func() { close(done) })
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("AfterFunc callback never ran")
	}
}

func TestRealAfterFuncStop(t *testing.T) {
	c := Real()
	ran := make(chan struct{}, 1)
	tm := c.AfterFunc(time.Hour, func() { ran <- struct{}{} })
	if !tm.Stop() {
		t.Fatal("Stop on a pending AfterFunc reported it already fired")
	}
	select {
	case <-ran:
		t.Fatal("AfterFunc callback ran after Stop")
	case <-time.After(20 * time.Millisecond):
	}
}
