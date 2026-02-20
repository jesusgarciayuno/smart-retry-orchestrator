package store

import (
	"sync"
	"time"

	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/domain"
)

// MemoryStore implements Store with in-memory storage protected by sync.RWMutex.
type MemoryStore struct {
	mu           sync.RWMutex
	transactions map[string]*domain.Transaction
	decisions    map[string][]domain.RetryDecision
	txOrder      []string // maintains insertion order
}

// NewMemoryStore creates a new in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		transactions: make(map[string]*domain.Transaction),
		decisions:    make(map[string][]domain.RetryDecision),
		txOrder:      make([]string, 0),
	}
}

func (s *MemoryStore) SaveTransaction(tx *domain.Transaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.transactions[tx.ID]; exists {
		return domain.ErrDuplicateID
	}

	clone := *tx
	s.transactions[tx.ID] = &clone
	s.txOrder = append(s.txOrder, tx.ID)
	return nil
}

func (s *MemoryStore) GetTransaction(id string) (*domain.Transaction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tx, ok := s.transactions[id]
	if !ok {
		return nil, domain.ErrNotFound
	}

	clone := *tx
	clone.RetryAttempts = make([]domain.RetryAttempt, len(tx.RetryAttempts))
	copy(clone.RetryAttempts, tx.RetryAttempts)
	return &clone, nil
}

func (s *MemoryStore) UpdateTransaction(tx *domain.Transaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.transactions[tx.ID]; !exists {
		return domain.ErrNotFound
	}

	clone := *tx
	clone.RetryAttempts = make([]domain.RetryAttempt, len(tx.RetryAttempts))
	copy(clone.RetryAttempts, tx.RetryAttempts)
	s.transactions[tx.ID] = &clone
	return nil
}

func (s *MemoryStore) ListTransactions(filter domain.TransactionFilter) ([]*domain.Transaction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*domain.Transaction

	for _, id := range s.txOrder {
		tx := s.transactions[id]

		if filter.Status != nil && tx.Status != *filter.Status {
			continue
		}
		if filter.Processor != nil && tx.OriginalEvent.Processor != *filter.Processor {
			continue
		}

		clone := *tx
		clone.RetryAttempts = make([]domain.RetryAttempt, len(tx.RetryAttempts))
		copy(clone.RetryAttempts, tx.RetryAttempts)
		result = append(result, &clone)
	}

	// Apply offset
	if filter.Offset > 0 {
		if filter.Offset >= len(result) {
			return []*domain.Transaction{}, nil
		}
		result = result[filter.Offset:]
	}

	// Apply limit
	if filter.Limit > 0 && len(result) > filter.Limit {
		result = result[:filter.Limit]
	}

	return result, nil
}

func (s *MemoryStore) SaveDecisionLog(decision domain.RetryDecision) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.decisions[decision.TransactionID] = append(s.decisions[decision.TransactionID], decision)
	return nil
}

func (s *MemoryStore) GetDecisionLogs(txnID string) ([]domain.RetryDecision, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	logs, ok := s.decisions[txnID]
	if !ok {
		return []domain.RetryDecision{}, nil
	}

	result := make([]domain.RetryDecision, len(logs))
	copy(result, logs)
	return result, nil
}

func (s *MemoryStore) GetAllTransactionsInRange(start, end time.Time) ([]*domain.Transaction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*domain.Transaction

	for _, id := range s.txOrder {
		tx := s.transactions[id]
		if (tx.CreatedAt.Equal(start) || tx.CreatedAt.After(start)) &&
			(tx.CreatedAt.Equal(end) || tx.CreatedAt.Before(end)) {
			clone := *tx
			clone.RetryAttempts = make([]domain.RetryAttempt, len(tx.RetryAttempts))
			copy(clone.RetryAttempts, tx.RetryAttempts)
			result = append(result, &clone)
		}
	}

	return result, nil
}

func (s *MemoryStore) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.transactions = make(map[string]*domain.Transaction)
	s.decisions = make(map[string][]domain.RetryDecision)
	s.txOrder = make([]string, 0)
	return nil
}
