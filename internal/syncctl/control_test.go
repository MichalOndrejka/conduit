package syncctl

import (
	"context"
	"errors"
	"testing"
	"time"
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
