package orchestrator

import (
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"time"

	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/classifier"
	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/domain"
	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/health"
	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/store"
)

const maxRetries = 3
const delayedRetrySeconds = 30.0

// RetryOrchestrator ties classifier, health tracker, and store together.
type RetryOrchestrator interface {
	ProcessFailure(event domain.FailureEvent) (*domain.ProcessFailureResult, error)
	RecordRetryOutcome(txnID string, processor domain.ProcessorName, success bool) error
	GetAdaptiveWeights() []domain.AdaptiveWeight
}

type orchestrator struct {
	mu              sync.RWMutex
	classifier      classifier.Classifier
	healthTracker   health.HealthTracker
	store           store.Store
	rng             *rand.Rand
	adaptiveData    map[domain.FailureReasonCode]map[domain.ProcessorName]*adaptiveRecord
}

type adaptiveRecord struct {
	successes int
	total     int
}

// New creates a new RetryOrchestrator.
func New(c classifier.Classifier, h health.HealthTracker, s store.Store) RetryOrchestrator {
	return &orchestrator{
		classifier:    c,
		healthTracker: h,
		store:         s,
		rng:           rand.New(rand.NewSource(42)),
		adaptiveData:  make(map[domain.FailureReasonCode]map[domain.ProcessorName]*adaptiveRecord),
	}
}

// NewWithRNG creates an orchestrator with a specific RNG for deterministic testing.
func NewWithRNG(c classifier.Classifier, h health.HealthTracker, s store.Store, rng *rand.Rand) RetryOrchestrator {
	return &orchestrator{
		classifier:    c,
		healthTracker: h,
		store:         s,
		rng:           rng,
		adaptiveData:  make(map[domain.FailureReasonCode]map[domain.ProcessorName]*adaptiveRecord),
	}
}

func (o *orchestrator) ProcessFailure(event domain.FailureEvent) (*domain.ProcessFailureResult, error) {
	// 1. Record the failure outcome in health tracker
	o.healthTracker.RecordOutcome(event.Processor, false, event.Timestamp)

	// 2. Classify the failure
	classification := o.classifier.Classify(event)

	// 3. Hard decline → no retry
	if classification.DeclineType == domain.HardDecline {
		tx := &domain.Transaction{
			ID:             event.TransactionID,
			OriginalEvent:  event,
			Classification: classification,
			Status:         domain.StatusHardDeclined,
			MaxRetries:     maxRetries,
			RetryAttempts:  []domain.RetryAttempt{},
			CreatedAt:      event.Timestamp,
			UpdatedAt:      event.Timestamp,
		}

		if err := o.store.SaveTransaction(tx); err != nil {
			return nil, fmt.Errorf("saving hard-declined transaction: %w", err)
		}

		return &domain.ProcessFailureResult{
			TransactionID:  event.TransactionID,
			Classification: classification,
			ShouldRetry:    false,
			FinalStatus:    domain.StatusHardDeclined,
			Reasoning:      classification.Reasoning,
		}, nil
	}

	// 4. Create transaction for soft decline
	tx := &domain.Transaction{
		ID:             event.TransactionID,
		OriginalEvent:  event,
		Classification: classification,
		Status:         domain.StatusRetrying,
		MaxRetries:     maxRetries,
		RetryAttempts:  []domain.RetryAttempt{},
		CreatedAt:      event.Timestamp,
		UpdatedAt:      event.Timestamp,
	}

	if err := o.store.SaveTransaction(tx); err != nil {
		return nil, fmt.Errorf("saving transaction: %w", err)
	}

	// 5. Execute retry chain
	result := &domain.ProcessFailureResult{
		TransactionID:  event.TransactionID,
		Classification: classification,
		ShouldRetry:    true,
	}

	o.executeRetryChain(tx, event, classification)

	result.RetryAttempts = tx.RetryAttempts
	result.FinalStatus = tx.Status
	result.TotalCost = tx.TotalCost
	result.Reasoning = o.buildChainReasoning(tx)

	return result, nil
}

func (o *orchestrator) executeRetryChain(tx *domain.Transaction, event domain.FailureEvent, classification domain.ClassificationResult) {
	for tx.RetryCount < maxRetries {
		attempt := o.planRetryAttempt(tx, event, classification)
		if attempt == nil {
			tx.Status = domain.StatusExhausted
			tx.UpdatedAt = time.Now()
			o.store.UpdateTransaction(tx)
			return
		}

		// Simulate the retry outcome
		success := o.simulateOutcome(attempt.Processor)
		attempt.Success = success

		tx.RetryAttempts = append(tx.RetryAttempts, *attempt)
		tx.RetryCount++
		tx.TotalCost += attempt.Cost
		tx.UpdatedAt = time.Now()

		// Record health outcome
		o.healthTracker.RecordOutcome(attempt.Processor, success, attempt.Timestamp)

		// Record adaptive data (Stretch A)
		o.recordAdaptive(event.FailureCode, attempt.Processor, success)

		// Log decision
		o.store.SaveDecisionLog(domain.RetryDecision{
			TransactionID:   tx.ID,
			AttemptNumber:   attempt.AttemptNumber,
			SourceProcessor: event.Processor,
			TargetProcessor: attempt.Processor,
			Strategy:        attempt.Strategy,
			Reasoning:       attempt.Reasoning,
			DelaySeconds:    attempt.DelaySeconds,
			Timestamp:       attempt.Timestamp,
		})

		if success {
			tx.Status = domain.StatusRecovered
			now := time.Now()
			tx.RecoveredAt = &now
			tx.UpdatedAt = now
			o.store.UpdateTransaction(tx)
			return
		}
	}

	tx.Status = domain.StatusExhausted
	tx.UpdatedAt = time.Now()
	o.store.UpdateTransaction(tx)
}

func (o *orchestrator) planRetryAttempt(tx *domain.Transaction, event domain.FailureEvent, classification domain.ClassificationResult) *domain.RetryAttempt {
	attemptNum := tx.RetryCount + 1
	strategy := classification.Strategy
	now := time.Now()

	var targetProcessor domain.ProcessorName
	var delaySeconds float64
	var reasoning string

	originalHealth := o.healthTracker.GetHealth(event.Processor)

	switch strategy {
	case domain.StrategyImmediate:
		if originalHealth.State == domain.StateDown {
			alt := o.selectBestAlternative(event.Processor, event.FailureCode)
			if alt == nil {
				return nil
			}
			targetProcessor = *alt
			altHealth := o.healthTracker.GetHealth(targetProcessor)
			reasoning = fmt.Sprintf(
				"Immediate retry rerouted: original processor %s is DOWN (failure rate: %.0f%%, %d failures in %d events). "+
					"Selected %s as alternative (state: %s, failure rate: %.0f%%, cost: $%.2f). "+
					"Adaptive success rate for %s on %s: %.0f%%.",
				event.Processor, originalHealth.FailureRate*100, originalHealth.Failures, originalHealth.TotalEvents,
				targetProcessor, altHealth.State, altHealth.FailureRate*100, domain.ProcessorCost[targetProcessor],
				event.FailureCode, targetProcessor, o.getAdaptiveRate(event.FailureCode, targetProcessor)*100)
		} else {
			targetProcessor = event.Processor
			reasoning = fmt.Sprintf(
				"Immediate retry on same processor %s — transient failure likely resolved. "+
					"Processor state: %s (failure rate: %.0f%%, %d events in window, cost: $%.2f). "+
					"Attempt %d of %d.",
				event.Processor, originalHealth.State, originalHealth.FailureRate*100,
				originalHealth.TotalEvents, domain.ProcessorCost[targetProcessor],
				attemptNum, maxRetries)
		}

	case domain.StrategyDelayed:
		delaySeconds = delayedRetrySeconds
		if originalHealth.State == domain.StateDown {
			alt := o.selectBestAlternative(event.Processor, event.FailureCode)
			if alt == nil {
				return nil
			}
			targetProcessor = *alt
			altHealth := o.healthTracker.GetHealth(targetProcessor)
			reasoning = fmt.Sprintf(
				"Delayed retry (%.0fs wait) rerouted: original processor %s is DOWN (failure rate: %.0f%%). "+
					"After delay, routing to %s (state: %s, failure rate: %.0f%%, cost: $%.2f). "+
					"Delay allows rate-limit window to reset before attempting on healthier processor.",
				delaySeconds, event.Processor, originalHealth.FailureRate*100,
				targetProcessor, altHealth.State, altHealth.FailureRate*100, domain.ProcessorCost[targetProcessor])
		} else {
			targetProcessor = event.Processor
			reasoning = fmt.Sprintf(
				"Delayed retry (%.0fs wait) on same processor %s — rate limit likely caused the decline. "+
					"Processor state: %s (failure rate: %.0f%%, cost: $%.2f). "+
					"The %.0f-second delay allows the processor's rate-limit window to reset. Attempt %d of %d.",
				delaySeconds, event.Processor, originalHealth.State,
				originalHealth.FailureRate*100, domain.ProcessorCost[targetProcessor],
				delaySeconds, attemptNum, maxRetries)
		}

	case domain.StrategyAlternativeProcessor:
		alt := o.selectBestAlternative(event.Processor, event.FailureCode)
		if alt == nil {
			return nil
		}
		targetProcessor = *alt
		altHealth := o.healthTracker.GetHealth(targetProcessor)
		reasoning = fmt.Sprintf(
			"Alternative processor retry: original %s declined with %s (state: %s, failure rate: %.0f%%). "+
				"Routing to %s (state: %s, failure rate: %.0f%%, cost: $%.2f) — "+
				"a different acquiring path may bypass the issuer-side block. "+
				"Adaptive success rate for %s on %s: %.0f%%.",
			event.Processor, event.FailureCode, originalHealth.State, originalHealth.FailureRate*100,
			targetProcessor, altHealth.State, altHealth.FailureRate*100, domain.ProcessorCost[targetProcessor],
			event.FailureCode, targetProcessor, o.getAdaptiveRate(event.FailureCode, targetProcessor)*100)
	}

	return &domain.RetryAttempt{
		AttemptNumber: attemptNum,
		Processor:     targetProcessor,
		Strategy:      strategy,
		Cost:          domain.ProcessorCost[targetProcessor],
		DelaySeconds:  delaySeconds,
		Reasoning:     reasoning,
		Timestamp:     now,
	}
}

func (o *orchestrator) selectBestAlternative(exclude domain.ProcessorName, failureCode domain.FailureReasonCode) *domain.ProcessorName {
	type candidate struct {
		name        domain.ProcessorName
		cost        float64
		failureRate float64
		adaptiveRate float64
	}

	var candidates []candidate
	for _, p := range domain.AllProcessors() {
		if p == exclude {
			continue
		}
		h := o.healthTracker.GetHealth(p)
		if h.State == domain.StateDown {
			continue
		}
		c := candidate{
			name:         p,
			cost:         domain.ProcessorCost[p],
			failureRate:  h.FailureRate,
			adaptiveRate: o.getAdaptiveRate(failureCode, p),
		}
		candidates = append(candidates, c)
	}

	if len(candidates) == 0 {
		// Fallback: pick cheapest even if down
		cheapest := domain.ProcessorC
		for _, p := range domain.AllProcessors() {
			if p == exclude {
				continue
			}
			if domain.ProcessorCost[p] < domain.ProcessorCost[cheapest] || cheapest == exclude {
				cheapest = p
			}
		}
		return &cheapest
	}

	// Sort: (1) adaptive success rate DESC, (2) lowest cost ASC, (3) lowest failure rate ASC
	sort.Slice(candidates, func(i, j int) bool {
		// Higher adaptive rate is better
		if candidates[i].adaptiveRate != candidates[j].adaptiveRate {
			return candidates[i].adaptiveRate > candidates[j].adaptiveRate
		}
		// Lower cost is better
		if candidates[i].cost != candidates[j].cost {
			return candidates[i].cost < candidates[j].cost
		}
		// Lower failure rate is better
		return candidates[i].failureRate < candidates[j].failureRate
	})

	return &candidates[0].name
}

// processorBaseSuccessRate defines the base success probability for each processor.
var processorBaseSuccessRate = map[domain.ProcessorName]float64{
	domain.ProcessorA: 0.85,
	domain.ProcessorB: 0.70,
	domain.ProcessorC: 0.65,
}

func (o *orchestrator) simulateOutcome(processor domain.ProcessorName) bool {
	o.mu.Lock()
	defer o.mu.Unlock()

	h := o.healthTracker.GetHealth(processor)

	// Start with processor-specific base rate
	baseRate, ok := processorBaseSuccessRate[processor]
	if !ok {
		baseRate = 0.75
	}

	// Adjust based on current health state
	var successProb float64
	switch h.State {
	case domain.StateHealthy:
		successProb = baseRate
	case domain.StateDegraded:
		successProb = baseRate * 0.50
	case domain.StateDown:
		successProb = baseRate * 0.15
	default:
		successProb = baseRate * 0.75
	}

	return o.rng.Float64() < successProb
}

func (o *orchestrator) recordAdaptive(code domain.FailureReasonCode, processor domain.ProcessorName, success bool) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.adaptiveData[code] == nil {
		o.adaptiveData[code] = make(map[domain.ProcessorName]*adaptiveRecord)
	}
	if o.adaptiveData[code][processor] == nil {
		o.adaptiveData[code][processor] = &adaptiveRecord{}
	}

	rec := o.adaptiveData[code][processor]
	rec.total++
	if success {
		rec.successes++
	}
}

func (o *orchestrator) getAdaptiveRate(code domain.FailureReasonCode, processor domain.ProcessorName) float64 {
	o.mu.RLock()
	defer o.mu.RUnlock()

	if o.adaptiveData[code] == nil {
		return 0.5 // default neutral
	}
	rec := o.adaptiveData[code][processor]
	if rec == nil || rec.total == 0 {
		return 0.5
	}
	return float64(rec.successes) / float64(rec.total)
}

func (o *orchestrator) RecordRetryOutcome(txnID string, processor domain.ProcessorName, success bool) error {
	tx, err := o.store.GetTransaction(txnID)
	if err != nil {
		return err
	}

	o.healthTracker.RecordOutcome(processor, success, time.Now())
	o.recordAdaptive(tx.OriginalEvent.FailureCode, processor, success)

	if success && tx.Status == domain.StatusRetrying {
		tx.Status = domain.StatusRecovered
		now := time.Now()
		tx.RecoveredAt = &now
		tx.UpdatedAt = now
	}
	return o.store.UpdateTransaction(tx)
}

func (o *orchestrator) GetAdaptiveWeights() []domain.AdaptiveWeight {
	o.mu.RLock()
	defer o.mu.RUnlock()

	var weights []domain.AdaptiveWeight
	for code, processors := range o.adaptiveData {
		w := domain.AdaptiveWeight{
			FailureCode: code,
			Weights:     make(map[domain.ProcessorName]float64),
			SampleCount: make(map[domain.ProcessorName]int),
		}
		for p, rec := range processors {
			if rec.total > 0 {
				w.Weights[p] = float64(rec.successes) / float64(rec.total)
				w.SampleCount[p] = rec.total
			}
		}
		weights = append(weights, w)
	}
	return weights
}

func (o *orchestrator) buildChainReasoning(tx *domain.Transaction) string {
	if len(tx.RetryAttempts) == 0 {
		return "No retry attempts were made for this transaction."
	}

	// Build a detailed narrative of the retry chain
	narrative := fmt.Sprintf("Transaction %s (failure code: %s, amount: $%.2f %s, original processor: %s). ",
		tx.ID, tx.OriginalEvent.FailureCode, tx.OriginalEvent.Amount, tx.OriginalEvent.Currency, tx.OriginalEvent.Processor)

	narrative += fmt.Sprintf("Classification: %s decline → strategy: %s. ",
		tx.Classification.DeclineType, tx.Classification.Strategy)

	// Describe each attempt
	for i, attempt := range tx.RetryAttempts {
		outcome := "FAILED"
		if attempt.Success {
			outcome = "SUCCEEDED"
		}
		narrative += fmt.Sprintf("Attempt %d: %s via %s (%s, cost: $%.2f",
			i+1, attempt.Strategy, attempt.Processor, outcome, attempt.Cost)
		if attempt.DelaySeconds > 0 {
			narrative += fmt.Sprintf(", delay: %.0fs", attempt.DelaySeconds)
		}
		narrative += "). "
	}

	// Final summary
	last := tx.RetryAttempts[len(tx.RetryAttempts)-1]
	if tx.Status == domain.StatusRecovered {
		narrative += fmt.Sprintf("OUTCOME: Recovered after %d attempt(s) — final success on %s. Total retry cost: $%.2f. Revenue of $%.2f %s recovered.",
			len(tx.RetryAttempts), last.Processor, tx.TotalCost, tx.OriginalEvent.Amount, tx.OriginalEvent.Currency)
	} else {
		narrative += fmt.Sprintf("OUTCOME: Exhausted all %d retry attempts — last failure on %s. Total retry cost: $%.2f. Revenue of $%.2f %s could not be recovered.",
			len(tx.RetryAttempts), last.Processor, tx.TotalCost, tx.OriginalEvent.Amount, tx.OriginalEvent.Currency)
	}

	return narrative
}
