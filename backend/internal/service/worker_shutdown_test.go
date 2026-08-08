package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWorkerShutdownBeforeStartIsIdempotent(t *testing.T) {
	svc := &Service{}
	if err := svc.ShutdownWorker(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := svc.ShutdownWorker(context.Background()); err != nil {
		t.Fatal(err)
	}
	svc.StartWorker()
	if svc.workerStarted {
		t.Fatal("worker must not start after shutdown has begun")
	}
}

func TestWorkerShutdownHonorsDrainContext(t *testing.T) {
	svc := &Service{}
	svc.workerLifecycleMu.Lock()
	svc.ensureWorkerLifecycleLocked()
	svc.workerStarted = true
	svc.workerLifecycleMu.Unlock()

	releaseDispatcher := make(chan struct{})
	go func() {
		<-svc.workerStop
		<-releaseDispatcher
		close(svc.workerDone)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := svc.ShutdownWorker(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ShutdownWorker() error = %v, want deadline exceeded", err)
	}
	close(releaseDispatcher)
	if err := svc.ShutdownWorker(context.Background()); err != nil {
		t.Fatal(err)
	}
}
