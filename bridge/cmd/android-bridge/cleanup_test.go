package main

import (
	"context"
	"testing"
	"time"
)

func TestStopAndWaitStopsTheRunFirst(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() {
		<-ctx.Done()
		close(ran)
	}()

	started := time.Now()
	stopAndWait(stop, ran, 10*time.Second)

	if waited := time.Since(started); waited > time.Second {
		t.Errorf("waited %v for a run that ends once stopped", waited)
	}
}

func TestStopAndWaitGivesUpOnARunThatHangs(t *testing.T) {
	started := time.Now()
	stopAndWait(func() {}, make(chan struct{}), 50*time.Millisecond)

	if waited := time.Since(started); waited < 50*time.Millisecond || waited > time.Second {
		t.Errorf("waited %v for a hung run, want about the limit", waited)
	}
}
