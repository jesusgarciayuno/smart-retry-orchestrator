package api

import (
	"fmt"
	"time"

	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/domain"
)

// FailureRequest represents a single failure event submission.
type FailureRequest struct {
	TransactionID string  `json:"transaction_id"`
	Amount        float64 `json:"amount"`
	Currency      string  `json:"currency"`
	FailureCode   string  `json:"failure_code"`
	Processor     string  `json:"processor"`
	Timestamp     string  `json:"timestamp,omitempty"`
}

// Validate checks required fields and returns a list of error messages.
func (r *FailureRequest) Validate() []string {
	var errs []string
	if r.TransactionID == "" {
		errs = append(errs, "transaction_id is required")
	}
	if r.Amount <= 0 {
		errs = append(errs, "amount must be greater than 0")
	}
	if r.Currency == "" {
		errs = append(errs, "currency is required")
	}
	validCurrencies := map[string]bool{"IDR": true, "MYR": true, "PHP": true, "USD": true, "EUR": true, "GBP": true, "SGD": true}
	if r.Currency != "" && !validCurrencies[r.Currency] {
		errs = append(errs, fmt.Sprintf("invalid currency: %s", r.Currency))
	}
	if r.FailureCode == "" {
		errs = append(errs, "failure_code is required")
	}
	if r.Processor == "" {
		errs = append(errs, "processor is required")
	}
	validProcessors := map[string]bool{
		string(domain.ProcessorA): true,
		string(domain.ProcessorB): true,
		string(domain.ProcessorC): true,
	}
	if r.Processor != "" && !validProcessors[r.Processor] {
		errs = append(errs, fmt.Sprintf("invalid processor: %s", r.Processor))
	}
	return errs
}

// ToEvent converts the request to a domain FailureEvent.
func (r *FailureRequest) ToEvent() domain.FailureEvent {
	ts := time.Now()
	if r.Timestamp != "" {
		if parsed, err := time.Parse(time.RFC3339, r.Timestamp); err == nil {
			ts = parsed
		}
	}
	return domain.FailureEvent{
		TransactionID: r.TransactionID,
		Amount:        r.Amount,
		Currency:      r.Currency,
		FailureCode:   domain.FailureReasonCode(r.FailureCode),
		Processor:     domain.ProcessorName(r.Processor),
		Timestamp:     ts,
	}
}

// BatchFailureRequest wraps multiple failure events.
type BatchFailureRequest struct {
	Events []FailureRequest `json:"events"`
}

// Validate checks the batch.
func (r *BatchFailureRequest) Validate() []string {
	if len(r.Events) == 0 {
		return []string{"events array is required and must not be empty"}
	}
	return nil
}

// RetryOutcomeRequest reports the outcome of a retry attempt.
type RetryOutcomeRequest struct {
	Processor string `json:"processor"`
	Success   bool   `json:"success"`
}

// Validate checks required fields.
func (r *RetryOutcomeRequest) Validate() []string {
	var errs []string
	if r.Processor == "" {
		errs = append(errs, "processor is required")
	}
	return errs
}
