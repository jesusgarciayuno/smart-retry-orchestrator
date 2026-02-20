package classifier

import "github.com/jesuslgarciah/smart-retry-orchestrator/internal/domain"

// Classifier maps failure events to classification results.
type Classifier interface {
	Classify(event domain.FailureEvent) domain.ClassificationResult
}

type classificationRule struct {
	declineType domain.DeclineType
	strategy    domain.RetryStrategy
	reasoning   string
}

var rules = map[domain.FailureReasonCode]classificationRule{
	// Hard declines — permanent conditions that cannot be resolved by retrying
	domain.CodeInsufficientFunds: {
		declineType: domain.HardDecline,
		strategy:    domain.StrategyDoNotRetry,
		reasoning: "INSUFFICIENT_FUNDS: The cardholder's account balance is below the transaction amount. " +
			"This is a permanent condition outside our control — retrying would produce the same result and waste processing fees.",
	},
	domain.CodeCardExpired: {
		declineType: domain.HardDecline,
		strategy:    domain.StrategyDoNotRetry,
		reasoning: "CARD_EXPIRED: The payment card has passed its expiration date and will be permanently rejected " +
			"by the issuing bank. A new card number is required from the cardholder.",
	},
	domain.CodeInvalidCard: {
		declineType: domain.HardDecline,
		strategy:    domain.StrategyDoNotRetry,
		reasoning: "INVALID_CARD: The card number fails validation checks (Luhn algorithm or BIN range). " +
			"This is a data entry error that cannot be resolved by retrying the same card number.",
	},
	domain.CodeFraudSuspected: {
		declineType: domain.HardDecline,
		strategy:    domain.StrategyDoNotRetry,
		reasoning: "FRAUD_SUSPECTED: The issuing bank's fraud detection system has flagged this transaction. " +
			"Retrying could trigger additional security alerts and may result in the card being blocked entirely.",
	},

	// Soft declines — transient conditions that may succeed on retry
	domain.CodeProcessorTimeout: {
		declineType: domain.SoftDecline,
		strategy:    domain.StrategyImmediate,
		reasoning: "PROCESSOR_TIMEOUT: The payment processor did not respond within the expected time window. " +
			"This is a transient infrastructure issue — the processor is likely available for immediate retry.",
	},
	domain.CodeNetworkError: {
		declineType: domain.SoftDecline,
		strategy:    domain.StrategyImmediate,
		reasoning: "NETWORK_ERROR: A connectivity failure occurred between our system and the processor. " +
			"This is a transient network issue unrelated to the transaction itself — immediate retry has a high success probability.",
	},
	domain.CodeIssuerUnavailable: {
		declineType: domain.SoftDecline,
		strategy:    domain.StrategyAlternativeProcessor,
		reasoning: "ISSUER_UNAVAILABLE: The cardholder's issuing bank is not responding. " +
			"Since this is bank-side, routing through an alternative processor that connects via a different acquiring bank may succeed.",
	},
	domain.CodeDoNotHonor: {
		declineType: domain.SoftDecline,
		strategy:    domain.StrategyAlternativeProcessor,
		reasoning: "DO_NOT_HONOR: Generic issuer refusal without a specific reason code. " +
			"This is often processor-path-specific rather than card-specific — routing through an alternative processor may take a different acquiring path and succeed.",
	},
	domain.CodeRateLimitExceeded: {
		declineType: domain.SoftDecline,
		strategy:    domain.StrategyDelayed,
		reasoning: "RATE_LIMIT_EXCEEDED: The processor has throttled our requests due to volume limits. " +
			"A 30-second delay allows the rate limit window to reset before retrying on the same processor.",
	},
}

// DefaultClassifier implements Classifier using a static rule map.
type DefaultClassifier struct{}

// NewClassifier creates a new DefaultClassifier.
func NewClassifier() *DefaultClassifier {
	return &DefaultClassifier{}
}

func (c *DefaultClassifier) Classify(event domain.FailureEvent) domain.ClassificationResult {
	rule, ok := rules[event.FailureCode]
	if !ok {
		return domain.ClassificationResult{
			DeclineType: domain.HardDecline,
			Strategy:    domain.StrategyDoNotRetry,
			FailureCode: event.FailureCode,
			Reasoning: "Unknown failure code encountered. As a fail-safe measure, unknown codes are treated as hard declines " +
				"to prevent unnecessary retry costs and potential duplicate charges.",
		}
	}

	return domain.ClassificationResult{
		DeclineType: rule.declineType,
		Strategy:    rule.strategy,
		FailureCode: event.FailureCode,
		Reasoning:   rule.reasoning,
	}
}
