package health

import (
	"sync"
	"time"

	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/domain"
)

const (
	defaultWindow           = 15 * time.Minute
	degradedThreshold       = 0.50
	downThreshold           = 0.80
	minSamplesForAssessment = 5
)

// HealthTracker tracks processor health using a rolling window.
type HealthTracker interface {
	RecordOutcome(processor domain.ProcessorName, success bool, ts time.Time)
	GetHealth(processor domain.ProcessorName) domain.ProcessorHealthSnapshot
	GetAllHealth() map[domain.ProcessorName]domain.ProcessorHealthSnapshot
	Reset()
}

type outcome struct {
	timestamp time.Time
	success   bool
}

type processorWindow struct {
	outcomes []outcome
}

// RollingHealthTracker implements HealthTracker with a 15-minute rolling window.
type RollingHealthTracker struct {
	mu      sync.RWMutex
	windows map[domain.ProcessorName]*processorWindow
	window  time.Duration
	nowFunc func() time.Time // injectable for testing
}

// NewHealthTracker creates a new RollingHealthTracker.
func NewHealthTracker() *RollingHealthTracker {
	return &RollingHealthTracker{
		windows: make(map[domain.ProcessorName]*processorWindow),
		window:  defaultWindow,
		nowFunc: time.Now,
	}
}

// NewHealthTrackerWithClock creates a tracker with a custom clock for testing.
func NewHealthTrackerWithClock(nowFunc func() time.Time) *RollingHealthTracker {
	return &RollingHealthTracker{
		windows: make(map[domain.ProcessorName]*processorWindow),
		window:  defaultWindow,
		nowFunc: nowFunc,
	}
}

func (h *RollingHealthTracker) RecordOutcome(processor domain.ProcessorName, success bool, ts time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()

	w, ok := h.windows[processor]
	if !ok {
		w = &processorWindow{}
		h.windows[processor] = w
	}

	w.outcomes = append(w.outcomes, outcome{timestamp: ts, success: success})

	// Lazy pruning under write lock: remove expired entries to bound memory
	cutoff := h.nowFunc().Add(-h.window)
	n := 0
	for _, o := range w.outcomes {
		if !o.timestamp.Before(cutoff) {
			w.outcomes[n] = o
			n++
		}
	}
	w.outcomes = w.outcomes[:n]
}

func (h *RollingHealthTracker) GetHealth(processor domain.ProcessorName) domain.ProcessorHealthSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return h.getHealthLocked(processor)
}

func (h *RollingHealthTracker) GetAllHealth() map[domain.ProcessorName]domain.ProcessorHealthSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()

	result := make(map[domain.ProcessorName]domain.ProcessorHealthSnapshot)
	for _, p := range domain.AllProcessors() {
		result[p] = h.getHealthLocked(p)
	}
	return result
}

func (h *RollingHealthTracker) getHealthLocked(processor domain.ProcessorName) domain.ProcessorHealthSnapshot {
	now := h.nowFunc()
	cutoff := now.Add(-h.window)

	snap := domain.ProcessorHealthSnapshot{
		Processor:   processor,
		WindowStart: cutoff,
		WindowEnd:   now,
	}

	w, ok := h.windows[processor]
	if !ok {
		snap.State = domain.StateHealthy
		return snap
	}

	// Count events in window (read-only — no mutation under RLock)
	for _, o := range w.outcomes {
		if o.timestamp.Before(cutoff) {
			continue
		}
		snap.TotalEvents++
		if o.success {
			snap.Successes++
		} else {
			snap.Failures++
		}
	}

	// Insufficient data → default healthy
	if snap.TotalEvents < minSamplesForAssessment {
		snap.State = domain.StateHealthy
		return snap
	}

	snap.FailureRate = float64(snap.Failures) / float64(snap.TotalEvents)

	switch {
	case snap.FailureRate >= downThreshold:
		snap.State = domain.StateDown
	case snap.FailureRate >= degradedThreshold:
		snap.State = domain.StateDegraded
	default:
		snap.State = domain.StateHealthy
	}

	return snap
}

// Reset clears all tracked data.
func (h *RollingHealthTracker) Reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.windows = make(map[domain.ProcessorName]*processorWindow)
}
