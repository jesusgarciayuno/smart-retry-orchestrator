package metrics

import (
	"time"

	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/domain"
	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/health"
	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/store"
)

// MetricsCalculator computes recovery and processor metrics.
type MetricsCalculator interface {
	GetRecoveryMetrics(start, end time.Time) (*domain.RecoveryMetrics, error)
	GetProcessorMetrics(start, end time.Time) (map[domain.ProcessorName]*domain.ProcessorMetrics, error)
}

type calculator struct {
	store         store.Store
	healthTracker health.HealthTracker
}

// NewCalculator creates a new MetricsCalculator.
func NewCalculator(s store.Store, h health.HealthTracker) MetricsCalculator {
	return &calculator{store: s, healthTracker: h}
}

func (c *calculator) GetRecoveryMetrics(start, end time.Time) (*domain.RecoveryMetrics, error) {
	txns, err := c.store.GetAllTransactionsInRange(start, end)
	if err != nil {
		return nil, err
	}

	m := &domain.RecoveryMetrics{
		Start:                start,
		End:                  end,
		FailureCodeBreakdown: make(map[domain.FailureReasonCode]int),
	}

	for _, tx := range txns {
		m.TotalTransactions++
		m.FailureCodeBreakdown[tx.OriginalEvent.FailureCode]++

		switch tx.Classification.DeclineType {
		case domain.HardDecline:
			m.HardDeclines++
		case domain.SoftDecline:
			m.SoftDeclines++
			m.TotalRevenueAtRisk += tx.OriginalEvent.Amount
		}

		switch tx.Status {
		case domain.StatusRecovered:
			m.Recovered++
			m.RevenueRecovered += tx.OriginalEvent.Amount
		case domain.StatusExhausted:
			m.Exhausted++
		}

		m.TotalRetryCost += tx.TotalCost
	}

	if m.SoftDeclines > 0 {
		m.RecoveryRate = float64(m.Recovered) / float64(m.SoftDeclines)
	}

	return m, nil
}

func (c *calculator) GetProcessorMetrics(start, end time.Time) (map[domain.ProcessorName]*domain.ProcessorMetrics, error) {
	txns, err := c.store.GetAllTransactionsInRange(start, end)
	if err != nil {
		return nil, err
	}

	result := make(map[domain.ProcessorName]*domain.ProcessorMetrics)
	for _, p := range domain.AllProcessors() {
		result[p] = &domain.ProcessorMetrics{
			Processor:     p,
			CurrentHealth: c.healthTracker.GetHealth(p),
		}
	}

	for _, tx := range txns {
		for _, attempt := range tx.RetryAttempts {
			pm, ok := result[attempt.Processor]
			if !ok {
				continue
			}
			pm.TotalAttempts++
			pm.TotalCost += attempt.Cost
			if attempt.Success {
				pm.Successes++
			} else {
				pm.Failures++
			}
		}
	}

	for _, pm := range result {
		if pm.TotalAttempts > 0 {
			pm.SuccessRate = float64(pm.Successes) / float64(pm.TotalAttempts)
		}
	}

	return result, nil
}
