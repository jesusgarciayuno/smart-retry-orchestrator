package domain

// FailureReasonCode represents the reason a payment failed.
type FailureReasonCode string

const (
	// Hard decline codes
	CodeInsufficientFunds FailureReasonCode = "INSUFFICIENT_FUNDS"
	CodeCardExpired       FailureReasonCode = "CARD_EXPIRED"
	CodeInvalidCard       FailureReasonCode = "INVALID_CARD"
	CodeFraudSuspected    FailureReasonCode = "FRAUD_SUSPECTED"

	// Soft decline codes
	CodeProcessorTimeout  FailureReasonCode = "PROCESSOR_TIMEOUT"
	CodeNetworkError      FailureReasonCode = "NETWORK_ERROR"
	CodeIssuerUnavailable FailureReasonCode = "ISSUER_UNAVAILABLE"
	CodeDoNotHonor        FailureReasonCode = "DO_NOT_HONOR"
	CodeRateLimitExceeded FailureReasonCode = "RATE_LIMIT_EXCEEDED"
)

// DeclineType indicates whether a decline is hard (permanent) or soft (retryable).
type DeclineType string

const (
	HardDecline DeclineType = "HARD_DECLINE"
	SoftDecline DeclineType = "SOFT_DECLINE"
)

// RetryStrategy indicates the retry approach for a soft decline.
type RetryStrategy string

const (
	StrategyDoNotRetry            RetryStrategy = "DO_NOT_RETRY"
	StrategyImmediate             RetryStrategy = "IMMEDIATE"
	StrategyDelayed               RetryStrategy = "DELAYED"
	StrategyAlternativeProcessor  RetryStrategy = "ALTERNATIVE_PROCESSOR"
)

// ProcessorState represents the health state of a processor.
type ProcessorState string

const (
	StateHealthy  ProcessorState = "HEALTHY"
	StateDegraded ProcessorState = "DEGRADED"
	StateDown     ProcessorState = "DOWN"
)

// ProcessorName identifies a payment processor.
type ProcessorName string

const (
	ProcessorA ProcessorName = "PROCESSOR_A"
	ProcessorB ProcessorName = "PROCESSOR_B"
	ProcessorC ProcessorName = "PROCESSOR_C"
)

// AllProcessors returns all available processor names.
func AllProcessors() []ProcessorName {
	return []ProcessorName{ProcessorA, ProcessorB, ProcessorC}
}

// TransactionStatus represents the current state of a transaction.
type TransactionStatus string

const (
	StatusPending      TransactionStatus = "PENDING"
	StatusHardDeclined TransactionStatus = "HARD_DECLINED"
	StatusRetrying     TransactionStatus = "RETRYING"
	StatusRecovered    TransactionStatus = "RECOVERED"
	StatusExhausted    TransactionStatus = "EXHAUSTED"
)
