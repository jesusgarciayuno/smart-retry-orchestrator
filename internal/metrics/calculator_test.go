package metrics

import (
	"fmt"
	"testing"
	"time"

	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/domain"
	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/health"
	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupCalc() (MetricsCalculator, *store.MemoryStore) {
	s := store.NewMemoryStore()
	h := health.NewHealthTracker()
	c := NewCalculator(s, h)
	return c, s
}

func TestRecoveryMetrics_Empty(t *testing.T) {
	c, _ := setupCalc()

	now := time.Now()
	m, err := c.GetRecoveryMetrics(now.Add(-time.Hour), now)
	require.NoError(t, err)

	assert.Equal(t, 0, m.TotalTransactions)
	assert.Equal(t, float64(0), m.RecoveryRate)
	assert.Equal(t, float64(0), m.RevenueRecovered)
}

func TestRecoveryMetrics_MixedTransactions(t *testing.T) {
	c, s := setupCalc()
	now := time.Now()

	// 2 hard declines
	for i := 0; i < 2; i++ {
		s.SaveTransaction(&domain.Transaction{
			ID: fmt.Sprintf("hard-%d", i),
			OriginalEvent: domain.FailureEvent{
				Amount:   100.00,
				Currency: "USD",
			},
			Classification: domain.ClassificationResult{DeclineType: domain.HardDecline},
			Status:         domain.StatusHardDeclined,
			CreatedAt:      now.Add(-30 * time.Minute),
		})
	}

	// 3 soft declines: 2 recovered, 1 exhausted
	s.SaveTransaction(&domain.Transaction{
		ID: "soft-recovered-1",
		OriginalEvent: domain.FailureEvent{
			Amount:   200.00,
			Currency: "USD",
		},
		Classification: domain.ClassificationResult{DeclineType: domain.SoftDecline},
		Status:         domain.StatusRecovered,
		TotalCost:      0.50,
		RetryAttempts: []domain.RetryAttempt{
			{Processor: domain.ProcessorA, Success: true, Cost: 0.30},
		},
		CreatedAt: now.Add(-20 * time.Minute),
	})

	s.SaveTransaction(&domain.Transaction{
		ID: "soft-recovered-2",
		OriginalEvent: domain.FailureEvent{
			Amount:   150.00,
			Currency: "USD",
		},
		Classification: domain.ClassificationResult{DeclineType: domain.SoftDecline},
		Status:         domain.StatusRecovered,
		TotalCost:      0.45,
		RetryAttempts: []domain.RetryAttempt{
			{Processor: domain.ProcessorB, Success: false, Cost: 0.25},
			{Processor: domain.ProcessorC, Success: true, Cost: 0.20},
		},
		CreatedAt: now.Add(-15 * time.Minute),
	})

	s.SaveTransaction(&domain.Transaction{
		ID: "soft-exhausted",
		OriginalEvent: domain.FailureEvent{
			Amount:   300.00,
			Currency: "USD",
		},
		Classification: domain.ClassificationResult{DeclineType: domain.SoftDecline},
		Status:         domain.StatusExhausted,
		TotalCost:      0.75,
		RetryAttempts: []domain.RetryAttempt{
			{Processor: domain.ProcessorA, Success: false, Cost: 0.30},
			{Processor: domain.ProcessorB, Success: false, Cost: 0.25},
			{Processor: domain.ProcessorC, Success: false, Cost: 0.20},
		},
		CreatedAt: now.Add(-10 * time.Minute),
	})

	m, err := c.GetRecoveryMetrics(now.Add(-time.Hour), now)
	require.NoError(t, err)

	assert.Equal(t, 5, m.TotalTransactions)
	assert.Equal(t, 2, m.HardDeclines)
	assert.Equal(t, 3, m.SoftDeclines)
	assert.Equal(t, 2, m.Recovered)
	assert.Equal(t, 1, m.Exhausted)
	assert.InDelta(t, 2.0/3.0, m.RecoveryRate, 0.01)
	assert.InDelta(t, 350.00, m.RevenueRecovered, 0.01)
	assert.InDelta(t, 650.00, m.TotalRevenueAtRisk, 0.01)
}

func TestProcessorMetrics_Aggregation(t *testing.T) {
	c, s := setupCalc()
	now := time.Now()

	s.SaveTransaction(&domain.Transaction{
		ID: "pm-test-1",
		OriginalEvent: domain.FailureEvent{
			Amount:   100.00,
			Currency: "USD",
		},
		Classification: domain.ClassificationResult{DeclineType: domain.SoftDecline},
		Status:         domain.StatusRecovered,
		RetryAttempts: []domain.RetryAttempt{
			{Processor: domain.ProcessorA, Success: false, Cost: 0.30},
			{Processor: domain.ProcessorB, Success: true, Cost: 0.25},
		},
		TotalCost: 0.55,
		CreatedAt: now.Add(-30 * time.Minute),
	})

	pm, err := c.GetProcessorMetrics(now.Add(-time.Hour), now)
	require.NoError(t, err)

	assert.Len(t, pm, 3)

	procA := pm[domain.ProcessorA]
	assert.Equal(t, 1, procA.TotalAttempts)
	assert.Equal(t, 0, procA.Successes)
	assert.Equal(t, 1, procA.Failures)
	assert.InDelta(t, 0.30, procA.TotalCost, 0.001)

	procB := pm[domain.ProcessorB]
	assert.Equal(t, 1, procB.TotalAttempts)
	assert.Equal(t, 1, procB.Successes)
	assert.InDelta(t, 1.0, procB.SuccessRate, 0.01)

	procC := pm[domain.ProcessorC]
	assert.Equal(t, 0, procC.TotalAttempts)
}

func TestRecoveryMetrics_TimeFiltering(t *testing.T) {
	c, s := setupCalc()
	now := time.Now()

	// Inside range
	s.SaveTransaction(&domain.Transaction{
		ID:             "in-range",
		OriginalEvent:  domain.FailureEvent{Amount: 100.00},
		Classification: domain.ClassificationResult{DeclineType: domain.SoftDecline},
		Status:         domain.StatusRecovered,
		CreatedAt:      now.Add(-30 * time.Minute),
	})

	// Outside range
	s.SaveTransaction(&domain.Transaction{
		ID:             "out-of-range",
		OriginalEvent:  domain.FailureEvent{Amount: 999.00},
		Classification: domain.ClassificationResult{DeclineType: domain.SoftDecline},
		Status:         domain.StatusRecovered,
		CreatedAt:      now.Add(-2 * time.Hour),
	})

	m, err := c.GetRecoveryMetrics(now.Add(-time.Hour), now)
	require.NoError(t, err)

	assert.Equal(t, 1, m.TotalTransactions, "should only include transactions in range")
}
