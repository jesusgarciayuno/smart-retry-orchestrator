package store

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// helper to build a minimal Transaction with sensible defaults.
func newTestTransaction(id string, processor domain.ProcessorName, status domain.TransactionStatus, createdAt time.Time) *domain.Transaction {
	return &domain.Transaction{
		ID: id,
		OriginalEvent: domain.FailureEvent{
			TransactionID: id,
			Amount:        100.00,
			Currency:      "USD",
			FailureCode:   domain.CodeProcessorTimeout,
			Processor:     processor,
			Timestamp:     createdAt,
		},
		Classification: domain.ClassificationResult{
			DeclineType: domain.SoftDecline,
			Strategy:    domain.StrategyImmediate,
			FailureCode: domain.CodeProcessorTimeout,
			Reasoning:   "soft decline – retryable",
		},
		Status:     status,
		RetryCount: 0,
		MaxRetries: 3,
		RetryAttempts: []domain.RetryAttempt{
			{
				AttemptNumber: 1,
				Processor:     processor,
				Strategy:      domain.StrategyImmediate,
				Success:       false,
				Cost:          0.30,
				DelaySeconds:  0,
				Reasoning:     "first attempt",
				Timestamp:     createdAt,
			},
		},
		TotalCost: 0.30,
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}
}

func TestSaveAndGetTransaction(t *testing.T) {
	s := NewMemoryStore()
	now := time.Now().Truncate(time.Millisecond)

	tx := newTestTransaction("txn-001", domain.ProcessorA, domain.StatusRetrying, now)

	err := s.SaveTransaction(tx)
	require.NoError(t, err)

	got, err := s.GetTransaction("txn-001")
	require.NoError(t, err)
	require.NotNil(t, got)

	// Verify all fields match.
	assert.Equal(t, tx.ID, got.ID)
	assert.Equal(t, tx.OriginalEvent.TransactionID, got.OriginalEvent.TransactionID)
	assert.Equal(t, tx.OriginalEvent.Amount, got.OriginalEvent.Amount)
	assert.Equal(t, tx.OriginalEvent.Currency, got.OriginalEvent.Currency)
	assert.Equal(t, tx.OriginalEvent.FailureCode, got.OriginalEvent.FailureCode)
	assert.Equal(t, tx.OriginalEvent.Processor, got.OriginalEvent.Processor)
	assert.Equal(t, tx.Classification.DeclineType, got.Classification.DeclineType)
	assert.Equal(t, tx.Classification.Strategy, got.Classification.Strategy)
	assert.Equal(t, tx.Classification.Reasoning, got.Classification.Reasoning)
	assert.Equal(t, tx.Status, got.Status)
	assert.Equal(t, tx.RetryCount, got.RetryCount)
	assert.Equal(t, tx.MaxRetries, got.MaxRetries)
	assert.Equal(t, tx.TotalCost, got.TotalCost)
	assert.Equal(t, tx.CreatedAt, got.CreatedAt)
	assert.Equal(t, tx.UpdatedAt, got.UpdatedAt)
	require.Len(t, got.RetryAttempts, 1)
	assert.Equal(t, tx.RetryAttempts[0].AttemptNumber, got.RetryAttempts[0].AttemptNumber)
	assert.Equal(t, tx.RetryAttempts[0].Processor, got.RetryAttempts[0].Processor)
	assert.Equal(t, tx.RetryAttempts[0].Cost, got.RetryAttempts[0].Cost)

	// Verify GetTransaction returns a clone (mutations do not affect stored data).
	got.Status = domain.StatusExhausted
	got2, err := s.GetTransaction("txn-001")
	require.NoError(t, err)
	assert.Equal(t, domain.StatusRetrying, got2.Status, "store should return a clone; mutation must not propagate")
}

func TestDuplicateID(t *testing.T) {
	s := NewMemoryStore()
	now := time.Now()

	tx := newTestTransaction("txn-dup", domain.ProcessorA, domain.StatusRetrying, now)

	err := s.SaveTransaction(tx)
	require.NoError(t, err)

	// Saving the same ID a second time must return ErrDuplicateID.
	err = s.SaveTransaction(tx)
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrDuplicateID), "expected ErrDuplicateID, got: %v", err)
}

func TestGetTransaction_NotFound(t *testing.T) {
	s := NewMemoryStore()

	got, err := s.GetTransaction("does-not-exist")
	require.Error(t, err)
	assert.Nil(t, got)
	assert.True(t, errors.Is(err, domain.ErrNotFound), "expected ErrNotFound, got: %v", err)
}

func TestUpdateTransaction(t *testing.T) {
	s := NewMemoryStore()
	now := time.Now().Truncate(time.Millisecond)

	tx := newTestTransaction("txn-upd", domain.ProcessorA, domain.StatusRetrying, now)
	require.NoError(t, s.SaveTransaction(tx))

	// Mutate the transaction and update.
	updatedAt := now.Add(5 * time.Second)
	tx.Status = domain.StatusRecovered
	tx.RetryCount = 2
	tx.TotalCost = 0.60
	tx.UpdatedAt = updatedAt
	tx.RetryAttempts = append(tx.RetryAttempts, domain.RetryAttempt{
		AttemptNumber: 2,
		Processor:     domain.ProcessorB,
		Strategy:      domain.StrategyAlternativeProcessor,
		Success:       true,
		Cost:          0.25,
		DelaySeconds:  0,
		Reasoning:     "recovered on alt processor",
		Timestamp:     updatedAt,
	})

	err := s.UpdateTransaction(tx)
	require.NoError(t, err)

	got, err := s.GetTransaction("txn-upd")
	require.NoError(t, err)

	assert.Equal(t, domain.StatusRecovered, got.Status)
	assert.Equal(t, 2, got.RetryCount)
	assert.InDelta(t, 0.60, got.TotalCost, 1e-9)
	assert.Equal(t, updatedAt, got.UpdatedAt)
	require.Len(t, got.RetryAttempts, 2)
	assert.Equal(t, domain.ProcessorB, got.RetryAttempts[1].Processor)
	assert.True(t, got.RetryAttempts[1].Success)
}

func TestUpdateTransaction_NotFound(t *testing.T) {
	s := NewMemoryStore()

	tx := newTestTransaction("ghost", domain.ProcessorA, domain.StatusRetrying, time.Now())
	err := s.UpdateTransaction(tx)
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrNotFound), "expected ErrNotFound, got: %v", err)
}

func TestListTransactions_All(t *testing.T) {
	s := NewMemoryStore()
	now := time.Now()

	for i := 0; i < 5; i++ {
		tx := newTestTransaction(fmt.Sprintf("txn-%d", i), domain.ProcessorA, domain.StatusRetrying, now.Add(time.Duration(i)*time.Second))
		require.NoError(t, s.SaveTransaction(tx))
	}

	result, err := s.ListTransactions(domain.TransactionFilter{})
	require.NoError(t, err)
	assert.Len(t, result, 5)

	// Verify insertion order is preserved.
	for i, tx := range result {
		assert.Equal(t, fmt.Sprintf("txn-%d", i), tx.ID)
	}
}

func TestListTransactions_FilterByStatus(t *testing.T) {
	s := NewMemoryStore()
	now := time.Now()

	statuses := []domain.TransactionStatus{
		domain.StatusRetrying,
		domain.StatusRecovered,
		domain.StatusExhausted,
		domain.StatusRecovered,
		domain.StatusHardDeclined,
	}

	for i, st := range statuses {
		tx := newTestTransaction(fmt.Sprintf("txn-%d", i), domain.ProcessorA, st, now.Add(time.Duration(i)*time.Second))
		require.NoError(t, s.SaveTransaction(tx))
	}

	recoveredStatus := domain.StatusRecovered
	result, err := s.ListTransactions(domain.TransactionFilter{Status: &recoveredStatus})
	require.NoError(t, err)
	assert.Len(t, result, 2)

	for _, tx := range result {
		assert.Equal(t, domain.StatusRecovered, tx.Status)
	}
}

func TestListTransactions_FilterByProcessor(t *testing.T) {
	s := NewMemoryStore()
	now := time.Now()

	processors := []domain.ProcessorName{
		domain.ProcessorA,
		domain.ProcessorB,
		domain.ProcessorA,
		domain.ProcessorC,
		domain.ProcessorA,
	}

	for i, proc := range processors {
		tx := newTestTransaction(fmt.Sprintf("txn-%d", i), proc, domain.StatusRetrying, now.Add(time.Duration(i)*time.Second))
		require.NoError(t, s.SaveTransaction(tx))
	}

	procA := domain.ProcessorA
	result, err := s.ListTransactions(domain.TransactionFilter{Processor: &procA})
	require.NoError(t, err)
	assert.Len(t, result, 3)

	for _, tx := range result {
		assert.Equal(t, domain.ProcessorA, tx.OriginalEvent.Processor)
	}
}

func TestListTransactions_Pagination(t *testing.T) {
	s := NewMemoryStore()
	now := time.Now()

	for i := 0; i < 10; i++ {
		tx := newTestTransaction(fmt.Sprintf("txn-%02d", i), domain.ProcessorA, domain.StatusRetrying, now.Add(time.Duration(i)*time.Second))
		require.NoError(t, s.SaveTransaction(tx))
	}

	result, err := s.ListTransactions(domain.TransactionFilter{Limit: 3, Offset: 2})
	require.NoError(t, err)
	assert.Len(t, result, 3)

	// Offset=2 means we skip the first 2, so IDs should be txn-02, txn-03, txn-04.
	assert.Equal(t, "txn-02", result[0].ID)
	assert.Equal(t, "txn-03", result[1].ID)
	assert.Equal(t, "txn-04", result[2].ID)

	// Edge case: offset beyond total count returns empty slice.
	result2, err := s.ListTransactions(domain.TransactionFilter{Offset: 100})
	require.NoError(t, err)
	assert.Empty(t, result2)

	// Edge case: limit larger than remaining items returns what is available.
	result3, err := s.ListTransactions(domain.TransactionFilter{Limit: 50, Offset: 8})
	require.NoError(t, err)
	assert.Len(t, result3, 2) // only txn-08 and txn-09 remain
}

func TestSaveAndGetDecisionLogs(t *testing.T) {
	s := NewMemoryStore()
	now := time.Now().Truncate(time.Millisecond)

	decisions := []domain.RetryDecision{
		{
			TransactionID:   "txn-log",
			AttemptNumber:   1,
			SourceProcessor: domain.ProcessorA,
			TargetProcessor: domain.ProcessorB,
			Strategy:        domain.StrategyAlternativeProcessor,
			Reasoning:       "processor A timed out, switching to B",
			DelaySeconds:    0,
			Timestamp:       now,
		},
		{
			TransactionID:   "txn-log",
			AttemptNumber:   2,
			SourceProcessor: domain.ProcessorB,
			TargetProcessor: domain.ProcessorC,
			Strategy:        domain.StrategyAlternativeProcessor,
			Reasoning:       "processor B degraded, switching to C",
			DelaySeconds:    0,
			Timestamp:       now.Add(time.Second),
		},
		{
			TransactionID:   "txn-other",
			AttemptNumber:   1,
			SourceProcessor: domain.ProcessorA,
			TargetProcessor: domain.ProcessorA,
			Strategy:        domain.StrategyImmediate,
			Reasoning:       "immediate retry on same processor",
			DelaySeconds:    0,
			Timestamp:       now,
		},
	}

	for _, d := range decisions {
		require.NoError(t, s.SaveDecisionLog(d))
	}

	// Get logs for txn-log: should return exactly 2.
	logs, err := s.GetDecisionLogs("txn-log")
	require.NoError(t, err)
	require.Len(t, logs, 2)

	assert.Equal(t, 1, logs[0].AttemptNumber)
	assert.Equal(t, domain.ProcessorB, logs[0].TargetProcessor)
	assert.Equal(t, "processor A timed out, switching to B", logs[0].Reasoning)

	assert.Equal(t, 2, logs[1].AttemptNumber)
	assert.Equal(t, domain.ProcessorC, logs[1].TargetProcessor)

	// Get logs for txn-other: should return exactly 1.
	logsOther, err := s.GetDecisionLogs("txn-other")
	require.NoError(t, err)
	require.Len(t, logsOther, 1)
	assert.Equal(t, domain.StrategyImmediate, logsOther[0].Strategy)

	// Get logs for unknown ID: should return empty slice (not error).
	logsNone, err := s.GetDecisionLogs("no-such-id")
	require.NoError(t, err)
	assert.Empty(t, logsNone)
}

func TestGetAllTransactionsInRange(t *testing.T) {
	s := NewMemoryStore()
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	// Create transactions spread across different times.
	times := []time.Time{
		base,                        // 12:00 – in range
		base.Add(1 * time.Hour),     // 13:00 – in range
		base.Add(2 * time.Hour),     // 14:00 – in range (boundary)
		base.Add(3 * time.Hour),     // 15:00 – out of range
		base.Add(-1 * time.Hour),    // 11:00 – out of range
	}

	for i, ts := range times {
		tx := newTestTransaction(fmt.Sprintf("txn-%d", i), domain.ProcessorA, domain.StatusRetrying, ts)
		require.NoError(t, s.SaveTransaction(tx))
	}

	// Query range [12:00, 14:00] – inclusive on both ends per implementation.
	start := base
	end := base.Add(2 * time.Hour)

	result, err := s.GetAllTransactionsInRange(start, end)
	require.NoError(t, err)
	assert.Len(t, result, 3, "expected 3 transactions in [12:00, 14:00]")

	ids := make(map[string]bool)
	for _, tx := range result {
		ids[tx.ID] = true
	}
	assert.True(t, ids["txn-0"], "txn-0 at 12:00 should be in range")
	assert.True(t, ids["txn-1"], "txn-1 at 13:00 should be in range")
	assert.True(t, ids["txn-2"], "txn-2 at 14:00 should be in range")
	assert.False(t, ids["txn-3"], "txn-3 at 15:00 should NOT be in range")
	assert.False(t, ids["txn-4"], "txn-4 at 11:00 should NOT be in range")

	// Query with a range that matches nothing.
	noResult, err := s.GetAllTransactionsInRange(base.Add(10*time.Hour), base.Add(11*time.Hour))
	require.NoError(t, err)
	assert.Empty(t, noResult)
}

func TestReset(t *testing.T) {
	s := NewMemoryStore()
	now := time.Now()

	// Populate some transactions and decision logs.
	for i := 0; i < 3; i++ {
		tx := newTestTransaction(fmt.Sprintf("txn-%d", i), domain.ProcessorA, domain.StatusRetrying, now)
		require.NoError(t, s.SaveTransaction(tx))
	}
	require.NoError(t, s.SaveDecisionLog(domain.RetryDecision{
		TransactionID:   "txn-0",
		AttemptNumber:   1,
		SourceProcessor: domain.ProcessorA,
		TargetProcessor: domain.ProcessorB,
		Strategy:        domain.StrategyAlternativeProcessor,
		Reasoning:       "test decision",
		Timestamp:       now,
	}))

	// Verify data exists before reset.
	txList, err := s.ListTransactions(domain.TransactionFilter{})
	require.NoError(t, err)
	require.Len(t, txList, 3)

	logs, err := s.GetDecisionLogs("txn-0")
	require.NoError(t, err)
	require.Len(t, logs, 1)

	// Reset.
	err = s.Reset()
	require.NoError(t, err)

	// Verify transactions are cleared.
	txList, err = s.ListTransactions(domain.TransactionFilter{})
	require.NoError(t, err)
	assert.Empty(t, txList)

	// Verify individual get returns not found.
	_, err = s.GetTransaction("txn-0")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrNotFound))

	// Verify decision logs are cleared.
	logs, err = s.GetDecisionLogs("txn-0")
	require.NoError(t, err)
	assert.Empty(t, logs)

	// Verify we can save new data after reset (maps are properly re-initialized).
	tx := newTestTransaction("txn-after-reset", domain.ProcessorB, domain.StatusRecovered, now)
	require.NoError(t, s.SaveTransaction(tx))

	got, err := s.GetTransaction("txn-after-reset")
	require.NoError(t, err)
	assert.Equal(t, "txn-after-reset", got.ID)
}
