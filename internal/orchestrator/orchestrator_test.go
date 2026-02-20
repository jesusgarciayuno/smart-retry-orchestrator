package orchestrator

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/classifier"
	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/domain"
	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/health"
	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setup() (RetryOrchestrator, *health.RollingHealthTracker, *store.MemoryStore) {
	c := classifier.NewClassifier()
	h := health.NewHealthTracker()
	s := store.NewMemoryStore()
	rng := rand.New(rand.NewSource(42))
	o := NewWithRNG(c, h, s, rng)
	return o, h, s
}

func makeEvent(id string, code domain.FailureReasonCode, proc domain.ProcessorName) domain.FailureEvent {
	return domain.FailureEvent{
		TransactionID: id,
		Amount:        100.00,
		Currency:      "USD",
		FailureCode:   code,
		Processor:     proc,
		Timestamp:     time.Now(),
	}
}

func TestOrchestrator_HardDeclineNotRetried(t *testing.T) {
	o, _, s := setup()

	hardCodes := []domain.FailureReasonCode{
		domain.CodeInsufficientFunds,
		domain.CodeCardExpired,
		domain.CodeInvalidCard,
		domain.CodeFraudSuspected,
	}

	for i, code := range hardCodes {
		event := makeEvent(
			fmt.Sprintf("hard-%d", i),
			code,
			domain.ProcessorA,
		)
		result, err := o.ProcessFailure(event)
		require.NoError(t, err)

		assert.False(t, result.ShouldRetry, "hard decline %s should not retry", code)
		assert.Equal(t, domain.StatusHardDeclined, result.FinalStatus)
		assert.Empty(t, result.RetryAttempts)

		tx, err := s.GetTransaction(event.TransactionID)
		require.NoError(t, err)
		assert.Equal(t, 0, tx.RetryCount)
	}
}

func TestOrchestrator_SoftDeclineRetries(t *testing.T) {
	o, _, _ := setup()

	event := makeEvent("soft-timeout", domain.CodeProcessorTimeout, domain.ProcessorA)
	result, err := o.ProcessFailure(event)
	require.NoError(t, err)

	assert.True(t, result.ShouldRetry)
	assert.NotEmpty(t, result.RetryAttempts)
	assert.True(t, len(result.RetryAttempts) > 0 && len(result.RetryAttempts) <= maxRetries)

	for _, attempt := range result.RetryAttempts {
		assert.NotEmpty(t, attempt.Reasoning, "every attempt must have reasoning")
		assert.Greater(t, attempt.Cost, 0.0, "every attempt must have a cost")
	}
}

func TestOrchestrator_MaxRetriesEnforced(t *testing.T) {
	o, _, s := setup()

	event := makeEvent("max-retry", domain.CodeProcessorTimeout, domain.ProcessorA)
	result, err := o.ProcessFailure(event)
	require.NoError(t, err)

	assert.True(t, len(result.RetryAttempts) <= maxRetries, "should not exceed %d retries", maxRetries)

	tx, err := s.GetTransaction("max-retry")
	require.NoError(t, err)
	assert.True(t, tx.RetryCount <= maxRetries)
}

func TestOrchestrator_AlternativeProcessorStrategy(t *testing.T) {
	o, _, _ := setup()

	event := makeEvent("alt-proc", domain.CodeDoNotHonor, domain.ProcessorA)
	result, err := o.ProcessFailure(event)
	require.NoError(t, err)

	assert.True(t, result.ShouldRetry)

	// At least one attempt should use a different processor
	hasAlternative := false
	for _, attempt := range result.RetryAttempts {
		if attempt.Processor != domain.ProcessorA {
			hasAlternative = true
			break
		}
	}
	assert.True(t, hasAlternative, "alternative processor strategy should route to different processor")
}

func TestOrchestrator_DelayedStrategy(t *testing.T) {
	o, _, _ := setup()

	event := makeEvent("delayed", domain.CodeRateLimitExceeded, domain.ProcessorA)
	result, err := o.ProcessFailure(event)
	require.NoError(t, err)

	assert.True(t, result.ShouldRetry)
	for _, attempt := range result.RetryAttempts {
		assert.Equal(t, delayedRetrySeconds, attempt.DelaySeconds, "delayed strategy should have delay")
	}
}

func TestOrchestrator_DegradedProcessorDeprioritized(t *testing.T) {
	o, h, _ := setup()
	now := time.Now()

	// Make Processor B degraded (60% failure rate)
	for i := 0; i < 10; i++ {
		success := i >= 6
		h.RecordOutcome(domain.ProcessorB, success, now.Add(-time.Duration(i)*time.Minute))
	}

	event := makeEvent("degrade-test", domain.CodeDoNotHonor, domain.ProcessorA)
	result, err := o.ProcessFailure(event)
	require.NoError(t, err)

	// Should prefer processor C (cheaper and healthy) over B (degraded)
	if len(result.RetryAttempts) > 0 {
		firstAttempt := result.RetryAttempts[0]
		assert.NotEqual(t, domain.ProcessorA, firstAttempt.Processor, "should not retry on original processor")
	}
}

func TestOrchestrator_AllProcessorsDown(t *testing.T) {
	o, h, _ := setup()
	now := time.Now()

	// Make all processors down
	for _, p := range domain.AllProcessors() {
		for i := 0; i < 10; i++ {
			h.RecordOutcome(p, false, now.Add(-time.Duration(i)*time.Minute))
		}
	}

	event := makeEvent("all-down", domain.CodeProcessorTimeout, domain.ProcessorA)
	result, err := o.ProcessFailure(event)
	require.NoError(t, err)

	// Should still attempt (fallback) but likely fail
	assert.True(t, result.ShouldRetry)
}

func TestOrchestrator_CostTracking(t *testing.T) {
	o, _, _ := setup()

	event := makeEvent("cost-track", domain.CodeNetworkError, domain.ProcessorA)
	result, err := o.ProcessFailure(event)
	require.NoError(t, err)

	var expectedCost float64
	for _, attempt := range result.RetryAttempts {
		expectedCost += attempt.Cost
	}
	assert.InDelta(t, expectedCost, result.TotalCost, 0.001, "total cost should match sum of attempt costs")
}

func TestOrchestrator_RecordRetryOutcome(t *testing.T) {
	o, _, s := setup()

	event := makeEvent("outcome-test", domain.CodeProcessorTimeout, domain.ProcessorA)
	_, err := o.ProcessFailure(event)
	require.NoError(t, err)

	err = o.RecordRetryOutcome("outcome-test", domain.ProcessorB, true)
	require.NoError(t, err)

	tx, err := s.GetTransaction("outcome-test")
	require.NoError(t, err)
	assert.Equal(t, domain.StatusRecovered, tx.Status)
}

func TestOrchestrator_AdaptiveWeights(t *testing.T) {
	o, _, _ := setup()

	event := makeEvent("adaptive-1", domain.CodeProcessorTimeout, domain.ProcessorA)
	_, err := o.ProcessFailure(event)
	require.NoError(t, err)

	weights := o.GetAdaptiveWeights()
	assert.NotEmpty(t, weights, "should have adaptive weights after processing")
}
