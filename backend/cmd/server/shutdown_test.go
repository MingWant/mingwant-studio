package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRequestDrainTrackerRejectsNewWorkAndWaitsForActiveRequest(t *testing.T) {
	tracker := newRequestDrainTracker()
	if !tracker.beginRequest() {
		t.Fatal("request should be accepted before drain")
	}
	done := tracker.BeginDrain()
	if tracker.beginRequest() {
		t.Fatal("new request must be rejected after drain begins")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := waitForRequestDrain(ctx, done); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waitForRequestDrain() error = %v, want deadline exceeded", err)
	}
	tracker.endRequest()
	if err := waitForRequestDrain(context.Background(), done); err != nil {
		t.Fatal(err)
	}
	if tracker.BeginDrain() != done {
		t.Fatal("repeated drain must reuse the completion signal")
	}
}
