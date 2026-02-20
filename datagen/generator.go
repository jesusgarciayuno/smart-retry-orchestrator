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

// cardProfile ties a card network to its realistic BIN prefixes.
type cardProfile struct {
	CardType string
	BINs     []string
}

var cardProfiles = []cardProfile{
	{CardType: "VISA", BINs: []string{"411111", "421234", "456789", "400000", "413456"}},
	{CardType: "MASTERCARD", BINs: []string{"520000", "540123", "555555", "510567", "523456"}},
	{CardType: "AMEX", BINs: []string{"340000", "370012", "341234", "379999"}},
}

// countries contains ISO 3166-1 alpha-2 codes weighted toward SEA (the target
// market), with a small share of other common issuing countries.
var countries = []string{"ID", "ID", "ID", "MY", "MY", "PH", "PH", "SG", "US"}

// processorWeight controls the weighted distribution of events across processors.
type processorWeight struct {
	Processor domain.ProcessorName
	Weight    int // relative weight out of total sum
}

// weightedProcessors yields ~45% A, ~30% B, ~25% C.
var weightedProcessors = []processorWeight{
	{Processor: domain.ProcessorA, Weight: 45},
	{Processor: domain.ProcessorB, Weight: 30},
	{Processor: domain.ProcessorC, Weight: 25},
}

// pickWeightedProcessor selects a processor using the weighted distribution.
func pickWeightedProcessor(rng *rand.Rand) domain.ProcessorName {
	total := 0
	for _, pw := range weightedProcessors {
		total += pw.Weight
	}
	r := rng.Intn(total)
	cumulative := 0
	for _, pw := range weightedProcessors {
		cumulative += pw.Weight
		if r < cumulative {
			return pw.Processor
		}
	}
	return domain.ProcessorA // fallback
}

// pickCard returns a random card type and matching BIN.
func pickCard(rng *rand.Rand) (string, string) {
	profile := cardProfiles[rng.Intn(len(cardProfiles))]
	bin := profile.BINs[rng.Intn(len(profile.BINs))]
	return profile.CardType, bin
}

// GenerateEvents creates 210 deterministic test events.
//
// Processor distribution:
//   - Processor A: ~45% of events, healthy (~10% baseline failure rate)
//   - Processor B: ~30% of events, moderate issues (~25% failure rate)
//   - Processor C: ~25% of events, severe degradation (~70% failure rate
//     during a 10-minute window, then recovers)
//
// Hard declines (40%) are spread across all processors via the weighted
// distribution.  Soft declines (60%) are biased so that Processor B receives a
// larger share and Processor C clusters most of its soft declines inside the
// 10-minute degradation window (minutes 30-40).
func GenerateEvents() []domain.FailureEvent {
	rng := rand.New(rand.NewSource(seed))

	hardCount := int(float64(totalCount) * hardPct) // 84
	softCount := totalCount - hardCount              // 126

	events := make([]domain.FailureEvent, 0, totalCount)

	baseTime := time.Now().Add(-2 * time.Hour)

	// Degradation window for Processor C: 30min-40min from base
	degradeStart := baseTime.Add(30 * time.Minute)
	degradeEnd := baseTime.Add(40 * time.Minute)

	idx := 0

	// ---------------------------------------------------------------
	// Generate hard decline events (40% = 84 events)
	// Hard declines are processor-independent; use weighted assignment.
	// ---------------------------------------------------------------
	for i := 0; i < hardCount; i++ {
		proc := pickWeightedProcessor(rng)
		code := hardCodes[rng.Intn(len(hardCodes))]
		amount := 5.0 + rng.Float64()*495.0 // $5-$500
		currency := currencies[rng.Intn(len(currencies))]
		jitter := time.Duration(rng.Intn(7200)) * time.Second
		ts := baseTime.Add(jitter)
		cardType, bin := pickCard(rng)
		country := countries[rng.Intn(len(countries))]

		events = append(events, domain.FailureEvent{
			TransactionID: fmt.Sprintf("txn-%06d", idx),
			Amount:        float64(int(amount*100)) / 100, // 2 decimal places
			Currency:      currency,
			FailureCode:   code,
			Processor:     proc,
			CardType:      cardType,
			BIN:           bin,
			Country:       country,
			Timestamp:     ts,
		})
		idx++
	}

	// ---------------------------------------------------------------
	// Generate soft decline events (60% = 126 events)
	//
	// Soft-decline processor assignment is biased to produce the desired
	// failure-rate profile:
	//   - Processor B gets a moderately larger share of soft declines so
	//     that its observed failure rate rises to ~25% during retries.
	//   - Processor C gets its soft declines clustered in the degradation
	//     window so that, during those 10 minutes, its failure rate spikes
	//     to ~70%.
	//   - Processor A absorbs the plurality of soft declines but retries
	//     most successfully, keeping its failure rate around ~10%.
	//
	// Soft-decline processor weights: A=40, B=35, C=25
	// These are deliberately close to the hard-decline weights so the
	// *overall* per-processor totals land near 45%/30%/25%.
	// ---------------------------------------------------------------
	softProcessorWeights := []processorWeight{
		{Processor: domain.ProcessorA, Weight: 40},
		{Processor: domain.ProcessorB, Weight: 35},
		{Processor: domain.ProcessorC, Weight: 25},
	}

	pickSoftProcessor := func() domain.ProcessorName {
		total := 0
		for _, pw := range softProcessorWeights {
			total += pw.Weight
		}
		r := rng.Intn(total)
		cumulative := 0
		for _, pw := range softProcessorWeights {
			cumulative += pw.Weight
			if r < cumulative {
				return pw.Processor
			}
		}
		return domain.ProcessorA
	}

	for i := 0; i < softCount; i++ {
		proc := pickSoftProcessor()
		code := softCodes[rng.Intn(len(softCodes))]
		amount := 5.0 + rng.Float64()*495.0
		currency := currencies[rng.Intn(len(currencies))]
		cardType, bin := pickCard(rng)
		country := countries[rng.Intn(len(countries))]

		var ts time.Time
		// Processor C: 80% of its soft declines land in the 10-min degradation
		// window, producing the ~70% spike in that interval.
		if proc == domain.ProcessorC && rng.Float64() < 0.80 {
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
			CardType:      cardType,
			BIN:           bin,
			Country:       country,
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
