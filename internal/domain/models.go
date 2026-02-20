package domain

import "time"

// FailureEvent represents an incoming payment failure notification.
type FailureEvent struct {
	TransactionID string            `json:"transaction_id"`
	Amount        float64           `json:"amount"`
	Currency      string            `json:"currency"`
	FailureCode   FailureReasonCode `json:"failure_code"`
	Processor     ProcessorName     `json:"processor"`
	Timestamp     time.Time         `json:"timestamp"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

// ClassificationResult is the output of the classification engine.
type ClassificationResult struct {
	DeclineType DeclineType       `json:"decline_type"`
	Strategy    RetryStrategy     `json:"strategy"`
	FailureCode FailureReasonCode `json:"failure_code"`
	Reasoning   string            `json:"reasoning"`
}

// Transaction tracks a payment through its retry lifecycle.
type Transaction struct {
	ID              string            `json:"id"`
	OriginalEvent   FailureEvent      `json:"original_event"`
	Classification  ClassificationResult `json:"classification"`
	Status          TransactionStatus `json:"status"`
	RetryCount      int               `json:"retry_count"`
	MaxRetries      int               `json:"max_retries"`
	RetryAttempts   []RetryAttempt    `json:"retry_attempts"`
	TotalCost       float64           `json:"total_cost"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
	RecoveredAt     *time.Time        `json:"recovered_at,omitempty"`
}

// RetryAttempt records a single retry attempt.
type RetryAttempt struct {
	AttemptNumber   int           `json:"attempt_number"`
	Processor       ProcessorName `json:"processor"`
	Strategy        RetryStrategy `json:"strategy"`
	Success         bool          `json:"success"`
	Cost            float64       `json:"cost"`
	DelaySeconds    float64       `json:"delay_seconds"`
	Reasoning       string        `json:"reasoning"`
	Timestamp       time.Time     `json:"timestamp"`
}

// RetryDecision logs the orchestrator's decision for auditing.
type RetryDecision struct {
	TransactionID   string            `json:"transaction_id"`
	AttemptNumber   int               `json:"attempt_number"`
	SourceProcessor ProcessorName     `json:"source_processor"`
	TargetProcessor ProcessorName     `json:"target_processor"`
	Strategy        RetryStrategy     `json:"strategy"`
	Reasoning       string            `json:"reasoning"`
	DelaySeconds    float64           `json:"delay_seconds"`
	Timestamp       time.Time         `json:"timestamp"`
}

// ProcessorHealthSnapshot captures processor health at a point in time.
type ProcessorHealthSnapshot struct {
	Processor    ProcessorName  `json:"processor"`
	State        ProcessorState `json:"state"`
	FailureRate  float64        `json:"failure_rate"`
	TotalEvents  int            `json:"total_events"`
	Failures     int            `json:"failures"`
	Successes    int            `json:"successes"`
	WindowStart  time.Time      `json:"window_start"`
	WindowEnd    time.Time      `json:"window_end"`
}

// ProcessorCost maps processor names to their per-attempt cost.
var ProcessorCost = map[ProcessorName]float64{
	ProcessorA: 0.30,
	ProcessorB: 0.25,
	ProcessorC: 0.20,
}

// ProcessFailureResult is the complete result of processing a failure event.
type ProcessFailureResult struct {
	TransactionID  string               `json:"transaction_id"`
	Classification ClassificationResult `json:"classification"`
	ShouldRetry    bool                 `json:"should_retry"`
	RetryAttempts  []RetryAttempt       `json:"retry_attempts,omitempty"`
	FinalStatus    TransactionStatus    `json:"final_status"`
	TotalCost      float64              `json:"total_cost"`
	Reasoning      string               `json:"reasoning"`
}

// RecoveryMetrics aggregates recovery statistics.
type RecoveryMetrics struct {
	TotalTransactions    int                        `json:"total_transactions"`
	HardDeclines         int                        `json:"hard_declines"`
	SoftDeclines         int                        `json:"soft_declines"`
	Recovered            int                        `json:"recovered"`
	Exhausted            int                        `json:"exhausted"`
	RecoveryRate         float64                    `json:"recovery_rate"`
	TotalRevenueAtRisk   float64                    `json:"total_revenue_at_risk"`
	RevenueRecovered     float64                    `json:"revenue_recovered"`
	TotalRetryCost       float64                    `json:"total_retry_cost"`
	FailureCodeBreakdown map[FailureReasonCode]int  `json:"failure_code_breakdown"`
	Start                time.Time                  `json:"start"`
	End                  time.Time                  `json:"end"`
}

// ProcessorMetrics provides per-processor breakdown.
type ProcessorMetrics struct {
	Processor       ProcessorName  `json:"processor"`
	TotalAttempts   int            `json:"total_attempts"`
	Successes       int            `json:"successes"`
	Failures        int            `json:"failures"`
	SuccessRate     float64        `json:"success_rate"`
	TotalCost       float64        `json:"total_cost"`
	CurrentHealth   ProcessorHealthSnapshot `json:"current_health"`
}

// TransactionFilter specifies criteria for listing transactions.
type TransactionFilter struct {
	Status    *TransactionStatus `json:"status,omitempty"`
	Processor *ProcessorName     `json:"processor,omitempty"`
	Limit     int                `json:"limit,omitempty"`
	Offset    int                `json:"offset,omitempty"`
}

// AdaptiveWeight tracks per-error-code per-processor success rates.
type AdaptiveWeight struct {
	FailureCode FailureReasonCode         `json:"failure_code"`
	Weights     map[ProcessorName]float64 `json:"weights"`
	SampleCount map[ProcessorName]int     `json:"sample_count"`
}
