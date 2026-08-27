package syncctl

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MichalOndrejka/conduit/internal/models"
)

func TestCheckpointPassesWhenRunning(t *testing.T) {
	c := NewControlStore()
	c.Register("s1")
	if err := c.Checkpoint(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
}

func TestCancelRaisesAtCheckpoint(t *testing.T) {
	c := NewControlStore()
	c.Register("s1")
	c.RequestCancel("s1")
	if err := c.Checkpoint(context.Background(), "s1"); !errors.Is(err, ErrSyncCancelled) {
		t.Fatalf("err = %v, want ErrSyncCancelled", err)
	}
}

func TestPauseBlocksUntilResume(t *testing.T) {
	c := NewControlStore()
	c.Register("s1")
	c.Pause("s1")
	if !c.IsPaused("s1") {
		t.Fatal("not paused")
	}

	done := make(chan error, 1)
	go func() { done <- c.Checkpoint(context.Background(), "s1") }()

	select {
	case <-done:
		t.Fatal("checkpoint returned while paused")
	case <-time.After(50 * time.Millisecond):
	}

	c.Resume("s1")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("checkpoint did not unblock after resume")
	}
}

func TestCancelUnblocksPausedCheckpoint(t *testing.T) {
	c := NewControlStore()
	c.Register("s1")
	c.Pause("s1")

	done := make(chan error, 1)
	go func() { done <- c.Checkpoint(context.Background(), "s1") }()

	c.RequestCancel("s1")
	select {
	case err := <-done:
		if !errors.Is(err, ErrSyncCancelled) {
			t.Fatalf("err = %v, want ErrSyncCancelled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not unblock paused checkpoint")
	}
}

func TestContextCancellation(t *testing.T) {
	c := NewControlStore()
	c.Register("s1")
	c.Pause("s1")
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- c.Checkpoint(ctx, "s1") }()

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("context cancel did not unblock checkpoint")
	}
}

func TestRegisterResetsState(t *testing.T) {
	c := NewControlStore()
	c.Register("s1")
	c.RequestCancel("s1")
	c.Register("s1") // new run
	if err := c.Checkpoint(context.Background(), "s1"); err != nil {
		t.Fatalf("stale cancel survived Register: %v", err)
	}
}

func TestIsPausedFalseForUnknownSource(t *testing.T) {
	c := NewControlStore()
	if c.IsPaused("unknown") {
		t.Fatal("expected unregistered source to report not paused")
	}
}

func TestRequestCancelWithoutRegisterCreatesState(t *testing.T) {
	c := NewControlStore()
	c.RequestCancel("s1") // no prior Register
	if err := c.Checkpoint(context.Background(), "s1"); !errors.Is(err, ErrSyncCancelled) {
		t.Fatalf("err = %v, want ErrSyncCancelled", err)
	}
}

func TestPauseWithoutRegisterCreatesState(t *testing.T) {
	c := NewControlStore()
	c.Pause("s1") // no prior Register
	if !c.IsPaused("s1") {
		t.Fatal("expected source to be paused")
	}
}

func TestResumeWithoutPauseIsNoop(t *testing.T) {
	c := NewControlStore()
	c.Register("s1")
	c.Resume("s1") // never paused
	if c.IsPaused("s1") {
		t.Fatal("resume without pause should not mark paused")
	}
	if err := c.Checkpoint(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
}

func TestPauseTwiceIsIdempotent(t *testing.T) {
	c := NewControlStore()
	c.Register("s1")
	c.Pause("s1")
	c.Pause("s1") // already paused, should not reset resumeCh
	if !c.IsPaused("s1") {
		t.Fatal("expected source to remain paused")
	}
	c.Resume("s1")
	if err := c.Checkpoint(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
}

func TestRequestCancelWhileNotPausedDoesNotClosePausedChannel(t *testing.T) {
	c := NewControlStore()
	c.Register("s1")
	c.RequestCancel("s1") // not paused, should just set cancelled
	if c.IsPaused("s1") {
		t.Fatal("expected source to remain unpaused")
	}
	if err := c.Checkpoint(context.Background(), "s1"); !errors.Is(err, ErrSyncCancelled) {
		t.Fatalf("err = %v, want ErrSyncCancelled", err)
	}
}

func TestControlStoreClearRemovesState(t *testing.T) {
	c := NewControlStore()
	c.Register("s1")
	c.RequestCancel("s1")
	c.Clear("s1")

	// After Clear, a fresh state is created on next access, so the stale
	// cancel should not resurface.
	if err := c.Checkpoint(context.Background(), "s1"); err != nil {
		t.Fatalf("stale cancel survived Clear: %v", err)
	}
}

func TestProgressStoreSetGetClear(t *testing.T) {
	p := NewProgressStore()

	if _, ok := p.Get("s1"); ok {
		t.Fatal("expected no progress for unknown source")
	}

	want := models.SyncProgress{Phase: "fetching", Current: 1, Total: 10, Message: "in progress"}
	p.Set("s1", want)

	got, ok := p.Get("s1")
	if !ok {
		t.Fatal("expected progress to be present after Set")
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	p.Clear("s1")
	if _, ok := p.Get("s1"); ok {
		t.Fatal("expected progress to be gone after Clear")
	}
}
