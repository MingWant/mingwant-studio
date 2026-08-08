package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRuntimeCoordinatorWaitsUntilChannelSlotIsReleased(t *testing.T) {
	coordinator := &runtimeCoordinator{instanceID: "test", localRate: map[string]localRateEntry{}, localSlots: map[string]map[string]time.Time{}}
	releaseFirst, acquired, err := coordinator.acquire(context.Background(), "channel:one", 1, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("first acquire = (%v, %v), want acquired", acquired, err)
	}

	result := make(chan error, 1)
	go func() {
		releaseSecond, waitErr := coordinator.acquireWithWait(context.Background(), "channel:one", 1, time.Minute)
		if waitErr == nil {
			releaseSecond()
		}
		result <- waitErr
	}()

	select {
	case err := <-result:
		t.Fatalf("second acquire returned before release: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	releaseFirst()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("second acquire after release: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second acquire did not resume after release")
	}
}

func TestRuntimeCoordinatorStopsWaitingWhenContextIsCancelled(t *testing.T) {
	coordinator := &runtimeCoordinator{instanceID: "test", localRate: map[string]localRateEntry{}, localSlots: map[string]map[string]time.Time{}}
	release, acquired, err := coordinator.acquire(context.Background(), "channel:one", 1, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("first acquire = (%v, %v), want acquired", acquired, err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := coordinator.acquireWithWait(ctx, "channel:one", 1, time.Minute); err == nil {
		t.Fatal("acquireWithWait() error = nil after cancellation")
	}
}

func TestRuntimeCoordinatorSweepsExpiredLocalState(t *testing.T) {
	now := time.Now()
	coordinator := &runtimeCoordinator{
		instanceID: "test",
		localRate: map[string]localRateEntry{
			"expired": {expiresAt: now.Add(-time.Minute), count: 1},
		},
		localSlots: map[string]map[string]time.Time{
			"expired": {"token": now.Add(-time.Minute)},
		},
	}
	allowed, err := coordinator.allow(context.Background(), "current", 1, time.Minute)
	if err != nil || !allowed {
		t.Fatalf("allow() = (%v, %v), want allowed", allowed, err)
	}
	if _, exists := coordinator.localRate["expired"]; exists {
		t.Fatal("expired rate key was not swept")
	}
	if _, exists := coordinator.localSlots["expired"]; exists {
		t.Fatal("expired slot scope was not swept")
	}
}

func TestRuntimeCoordinatorReleaseRemovesEmptyLocalScope(t *testing.T) {
	coordinator := &runtimeCoordinator{instanceID: "test", localRate: map[string]localRateEntry{}, localSlots: map[string]map[string]time.Time{}}
	release, acquired, err := coordinator.acquire(context.Background(), "channel:one", 1, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire() = (%v, %v), want acquired", acquired, err)
	}
	release()
	if _, exists := coordinator.localSlots["channel:one"]; exists {
		t.Fatal("released empty slot scope was not removed")
	}
}

func TestRuntimeCoordinatorRenewableLeaseKeepsSlotOccupied(t *testing.T) {
	coordinator := &runtimeCoordinator{instanceID: "test", localRate: map[string]localRateEntry{}, localSlots: map[string]map[string]time.Time{}}
	lease, acquired, err := coordinator.acquireLease(context.Background(), "workers", 1, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquireLease() = (%v, %v), want acquired", acquired, err)
	}
	defer lease.Release()
	previousExpiry := coordinator.localSlots["workers"][lease.token]
	if err := lease.Renew(context.Background(), 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	if renewedExpiry := coordinator.localSlots["workers"][lease.token]; !renewedExpiry.After(previousExpiry) {
		t.Fatalf("renewed expiry = %s, want after %s", renewedExpiry, previousExpiry)
	}
	if _, secondAcquired, err := coordinator.acquireLease(context.Background(), "workers", 1, time.Minute); err != nil || secondAcquired {
		t.Fatalf("second acquire = (%v, %v), want occupied", secondAcquired, err)
	}
}

func TestRuntimeCoordinatorCannotReviveExpiredLease(t *testing.T) {
	coordinator := &runtimeCoordinator{instanceID: "test", localRate: map[string]localRateEntry{}, localSlots: map[string]map[string]time.Time{}}
	lease, acquired, err := coordinator.acquireLease(context.Background(), "workers", 1, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquireLease() = (%v, %v), want acquired", acquired, err)
	}
	coordinator.localSlots["workers"][lease.token] = time.Now().Add(-time.Second)
	if err := lease.Renew(context.Background(), time.Minute); !errors.Is(err, errRuntimeSlotLeaseLost) {
		t.Fatalf("Renew() error = %v, want lease lost", err)
	}
	if _, exists := coordinator.localSlots["workers"]; exists {
		t.Fatal("expired lease scope was not removed")
	}
}
