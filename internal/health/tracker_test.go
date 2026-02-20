package health

import (
	"testing"
	"time"

	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/domain"
	"github.com/stretchr/testify/assert"
)

func TestHealthTracker_EmptyWindow(t *testing.T) {
	tracker := NewHealthTracker()
	snap := tracker.GetHealth(domain.ProcessorA)

	assert.Equal(t, domain.StateHealthy, snap.State)
	assert.Equal(t, 0, snap.TotalEvents)
	assert.Equal(t, float64(0), snap.FailureRate)
}

func TestHealthTracker_InsufficientSamples(t *testing.T) {
	now := time.Now()
	tracker := NewHealthTrackerWithClock(func() time.Time { return now })

	// 4 failures out of 4 events → still healthy due to < 5 samples
	for i := 0; i < 4; i++ {
		tracker.RecordOutcome(domain.ProcessorA, false, now.Add(-time.Duration(i)*time.Second))
	}

	snap := tracker.GetHealth(domain.ProcessorA)
	assert.Equal(t, domain.StateHealthy, snap.State, "should be healthy with < 5 samples")
	assert.Equal(t, 4, snap.TotalEvents)
}

func TestHealthTracker_HealthyState(t *testing.T) {
	now := time.Now()
	tracker := NewHealthTrackerWithClock(func() time.Time { return now })

	// 2 failures out of 10 → 20% failure rate → healthy
	for i := 0; i < 10; i++ {
		success := i >= 2 // first 2 fail, rest succeed
		tracker.RecordOutcome(domain.ProcessorA, success, now.Add(-time.Duration(i)*time.Minute))
	}

	snap := tracker.GetHealth(domain.ProcessorA)
	assert.Equal(t, domain.StateHealthy, snap.State)
	assert.InDelta(t, 0.20, snap.FailureRate, 0.01)
}

func TestHealthTracker_DegradedState(t *testing.T) {
	now := time.Now()
	tracker := NewHealthTrackerWithClock(func() time.Time { return now })

	// 6 failures out of 10 → 60% failure rate → degraded
	for i := 0; i < 10; i++ {
		success := i >= 6
		tracker.RecordOutcome(domain.ProcessorA, success, now.Add(-time.Duration(i)*time.Minute))
	}

	snap := tracker.GetHealth(domain.ProcessorA)
	assert.Equal(t, domain.StateDegraded, snap.State)
	assert.InDelta(t, 0.60, snap.FailureRate, 0.01)
}

func TestHealthTracker_DownState(t *testing.T) {
	now := time.Now()
	tracker := NewHealthTrackerWithClock(func() time.Time { return now })

	// 9 failures out of 10 → 90% failure rate → down
	for i := 0; i < 10; i++ {
		success := i == 0
		tracker.RecordOutcome(domain.ProcessorA, success, now.Add(-time.Duration(i)*time.Minute))
	}

	snap := tracker.GetHealth(domain.ProcessorA)
	assert.Equal(t, domain.StateDown, snap.State)
	assert.InDelta(t, 0.90, snap.FailureRate, 0.01)
}

func TestHealthTracker_WindowExpiry(t *testing.T) {
	now := time.Now()
	currentTime := now
	tracker := NewHealthTrackerWithClock(func() time.Time { return currentTime })

	// Record 10 failures 20 minutes ago (outside window)
	for i := 0; i < 10; i++ {
		tracker.RecordOutcome(domain.ProcessorA, false, now.Add(-20*time.Minute-time.Duration(i)*time.Second))
	}

	// Record 5 successes within window
	for i := 0; i < 5; i++ {
		tracker.RecordOutcome(domain.ProcessorA, true, now.Add(-time.Duration(i)*time.Minute))
	}

	snap := tracker.GetHealth(domain.ProcessorA)
	assert.Equal(t, domain.StateHealthy, snap.State)
	assert.Equal(t, 5, snap.TotalEvents, "expired events should be pruned")
	assert.Equal(t, 0, snap.Failures)
}

func TestHealthTracker_Recovery(t *testing.T) {
	now := time.Now()
	currentTime := now
	tracker := NewHealthTrackerWithClock(func() time.Time { return currentTime })

	// Start with 9/10 failures → down
	for i := 0; i < 10; i++ {
		success := i == 0
		tracker.RecordOutcome(domain.ProcessorA, success, now.Add(-time.Duration(i)*time.Minute))
	}

	snap := tracker.GetHealth(domain.ProcessorA)
	assert.Equal(t, domain.StateDown, snap.State)

	// Advance time so old events expire, then add successes
	currentTime = now.Add(16 * time.Minute)
	for i := 0; i < 10; i++ {
		tracker.RecordOutcome(domain.ProcessorA, true, currentTime.Add(-time.Duration(i)*time.Minute))
	}

	snap = tracker.GetHealth(domain.ProcessorA)
	assert.Equal(t, domain.StateHealthy, snap.State, "should recover after old failures expire")
}

func TestHealthTracker_GetAllHealth(t *testing.T) {
	tracker := NewHealthTracker()

	all := tracker.GetAllHealth()
	assert.Len(t, all, 3)
	assert.Contains(t, all, domain.ProcessorA)
	assert.Contains(t, all, domain.ProcessorB)
	assert.Contains(t, all, domain.ProcessorC)
}

func TestHealthTracker_BoundaryThresholds(t *testing.T) {
	now := time.Now()
	tracker := NewHealthTrackerWithClock(func() time.Time { return now })

	// Exactly 50% failure rate → degraded (>= 50%)
	for i := 0; i < 10; i++ {
		success := i%2 == 0
		tracker.RecordOutcome(domain.ProcessorB, success, now.Add(-time.Duration(i)*time.Minute))
	}

	snap := tracker.GetHealth(domain.ProcessorB)
	assert.Equal(t, domain.StateDegraded, snap.State)
	assert.InDelta(t, 0.50, snap.FailureRate, 0.01)
}

func TestHealthTracker_ExactDownThreshold(t *testing.T) {
	now := time.Now()
	tracker := NewHealthTrackerWithClock(func() time.Time { return now })

	// Exactly 80% failure rate → down (>= 80%)
	for i := 0; i < 10; i++ {
		success := i < 2
		tracker.RecordOutcome(domain.ProcessorC, success, now.Add(-time.Duration(i)*time.Minute))
	}

	snap := tracker.GetHealth(domain.ProcessorC)
	assert.Equal(t, domain.StateDown, snap.State)
	assert.InDelta(t, 0.80, snap.FailureRate, 0.01)
}
