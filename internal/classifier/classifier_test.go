package classifier

import (
	"testing"
	"time"

	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/domain"
	"github.com/stretchr/testify/assert"
)

func makeEvent(code domain.FailureReasonCode) domain.FailureEvent {
	return domain.FailureEvent{
		TransactionID: "txn-test-001",
		Amount:        100.00,
		Currency:      "USD",
		FailureCode:   code,
		Processor:     domain.ProcessorA,
		Timestamp:     time.Now(),
	}
}

func TestClassifier_AllCodes(t *testing.T) {
	c := NewClassifier()

	tests := []struct {
		name        string
		code        domain.FailureReasonCode
		wantType    domain.DeclineType
		wantStrat   domain.RetryStrategy
	}{
		// Hard declines
		{
			name:      "INSUFFICIENT_FUNDS → hard decline",
			code:      domain.CodeInsufficientFunds,
			wantType:  domain.HardDecline,
			wantStrat: domain.StrategyDoNotRetry,
		},
		{
			name:      "CARD_EXPIRED → hard decline",
			code:      domain.CodeCardExpired,
			wantType:  domain.HardDecline,
			wantStrat: domain.StrategyDoNotRetry,
		},
		{
			name:      "INVALID_CARD → hard decline",
			code:      domain.CodeInvalidCard,
			wantType:  domain.HardDecline,
			wantStrat: domain.StrategyDoNotRetry,
		},
		{
			name:      "FRAUD_SUSPECTED → hard decline",
			code:      domain.CodeFraudSuspected,
			wantType:  domain.HardDecline,
			wantStrat: domain.StrategyDoNotRetry,
		},
		// Soft declines
		{
			name:      "PROCESSOR_TIMEOUT → soft, immediate",
			code:      domain.CodeProcessorTimeout,
			wantType:  domain.SoftDecline,
			wantStrat: domain.StrategyImmediate,
		},
		{
			name:      "NETWORK_ERROR → soft, immediate",
			code:      domain.CodeNetworkError,
			wantType:  domain.SoftDecline,
			wantStrat: domain.StrategyImmediate,
		},
		{
			name:      "ISSUER_UNAVAILABLE → soft, alternative processor",
			code:      domain.CodeIssuerUnavailable,
			wantType:  domain.SoftDecline,
			wantStrat: domain.StrategyAlternativeProcessor,
		},
		{
			name:      "DO_NOT_HONOR → soft, alternative processor",
			code:      domain.CodeDoNotHonor,
			wantType:  domain.SoftDecline,
			wantStrat: domain.StrategyAlternativeProcessor,
		},
		{
			name:      "RATE_LIMIT_EXCEEDED → soft, delayed",
			code:      domain.CodeRateLimitExceeded,
			wantType:  domain.SoftDecline,
			wantStrat: domain.StrategyDelayed,
		},
		// Unknown code → hard decline (fail-safe)
		{
			name:      "UNKNOWN_CODE → hard decline (fail-safe)",
			code:      "UNKNOWN_CODE",
			wantType:  domain.HardDecline,
			wantStrat: domain.StrategyDoNotRetry,
		},
		// Empty code → hard decline (fail-safe)
		{
			name:      "empty code → hard decline (fail-safe)",
			code:      "",
			wantType:  domain.HardDecline,
			wantStrat: domain.StrategyDoNotRetry,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := c.Classify(makeEvent(tt.code))
			assert.Equal(t, tt.wantType, result.DeclineType)
			assert.Equal(t, tt.wantStrat, result.Strategy)
			assert.Equal(t, tt.code, result.FailureCode)
			assert.NotEmpty(t, result.Reasoning)
		})
	}
}
