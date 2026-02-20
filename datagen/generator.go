package datagen

import (
	"fmt"
	"math/rand"
	"time"

	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/domain"
)

const (
	seed       = 42
	totalCount = 210
	hardPct    = 0.40
)

var hardCodes = []domain.FailureReasonCode{
	domain.CodeInsufficientFunds,
	domain.CodeCardExpired,
	domain.CodeInvalidCard,
	domain.CodeFraudSuspected,
}

var softCodes = []domain.FailureReasonCode{
	domain.CodeProcessorTimeout,
	domain.CodeNetworkError,
	domain.CodeIssuerUnavailable,
	domain.CodeDoNotHonor,
	domain.CodeRateLimitExceeded,
}

var currencies = []string{"IDR", "MYR", "PHP"}

// GenerateEvents creates 210 deterministic test events.
func GenerateEvents() []domain.FailureEvent {
	rng := rand.New(rand.NewSource(seed))

	hardCount := int(float64(totalCount) * hardPct) // 84
	softCount := totalCount - hardCount              // 126

	events := make([]domain.FailureEvent, 0, totalCount)
	processors := domain.AllProcessors()

	baseTime := time.Now().Add(-2 * time.Hour)

	// Degradation window for Processor C: 30min-40min from base
	degradeStart := baseTime.Add(30 * time.Minute)
	degradeEnd := baseTime.Add(40 * time.Minute)

	idx := 0

	// Generate hard decline events
	for i := 0; i < hardCount; i++ {
		proc := processors[idx%len(processors)]
		code := hardCodes[rng.Intn(len(hardCodes))]
		amount := 5.0 + rng.Float64()*495.0 // $5-$500
		currency := currencies[rng.Intn(len(currencies))]
		jitter := time.Duration(rng.Intn(7200)) * time.Second
		ts := baseTime.Add(jitter)

		events = append(events, domain.FailureEvent{
			TransactionID: fmt.Sprintf("txn-%06d", idx),
			Amount:        float64(int(amount*100)) / 100, // 2 decimal places
			Currency:      currency,
			FailureCode:   code,
			Processor:     proc,
			Timestamp:     ts,
		})
		idx++
	}

	// Generate soft decline events
	for i := 0; i < softCount; i++ {
		proc := processors[idx%len(processors)]
		code := softCodes[rng.Intn(len(softCodes))]
		amount := 5.0 + rng.Float64()*495.0
		currency := currencies[rng.Intn(len(currencies))]

		var ts time.Time
		// 70% of Processor C events in degradation window
		if proc == domain.ProcessorC && rng.Float64() < 0.70 {
			windowDuration := degradeEnd.Sub(degradeStart)
			ts = degradeStart.Add(time.Duration(rng.Int63n(int64(windowDuration))))
		} else {
			jitter := time.Duration(rng.Intn(7200)) * time.Second
			ts = baseTime.Add(jitter)
		}

		events = append(events, domain.FailureEvent{
			TransactionID: fmt.Sprintf("txn-%06d", idx),
			Amount:        float64(int(amount*100)) / 100,
			Currency:      currency,
			FailureCode:   code,
			Processor:     proc,
			Timestamp:     ts,
		})
		idx++
	}

	// Fisher-Yates shuffle for realistic ordering
	for i := len(events) - 1; i > 0; i-- {
		j := rng.Intn(i + 1)
		events[i], events[j] = events[j], events[i]
	}

	return events
}
