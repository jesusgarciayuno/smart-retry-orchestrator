package store

import (
	"time"

	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/domain"
)

// Store defines the persistence abstraction for transactions and decisions.
type Store interface {
	SaveTransaction(tx *domain.Transaction) error
	GetTransaction(id string) (*domain.Transaction, error)
	UpdateTransaction(tx *domain.Transaction) error
	ListTransactions(filter domain.TransactionFilter) ([]*domain.Transaction, error)
	SaveDecisionLog(decision domain.RetryDecision) error
	GetDecisionLogs(txnID string) ([]domain.RetryDecision, error)
	GetAllTransactionsInRange(start, end time.Time) ([]*domain.Transaction, error)
	Reset() error
}
