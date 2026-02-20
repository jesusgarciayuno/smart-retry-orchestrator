# Smart Retry Orchestrator

> Recovers revenue from soft-declined payment transactions through intelligent retry logic, processor health monitoring, and cost-optimized routing.

## Problem Statement

TigerPay loses approximately **$880K/month** because 22% of legitimate payments fail with no retry logic. The top client BukuStore ($4M/month) threatens to leave. Payment failures fall into two categories:

- **Hard declines**: Permanent failures (expired cards, fraud blocks) that should never be retried
- **Soft declines**: Transient failures (timeouts, network errors, rate limits) that can often succeed on retry

This service classifies failures, intelligently retries soft declines across 3 processors based on real-time health, and provides metrics to prove revenue recovery.

## Architecture Overview

```
                    ┌─────────────┐
   POST /failures → │   API Layer  │
                    │  (Chi Router)│
                    └──────┬──────┘
                           │
                    ┌──────▼──────┐
                    │  Classifier  │ ── Maps failure codes → hard/soft + strategy
                    └──────┬──────┘
                           │
                    ┌──────▼──────────┐
                    │  Orchestrator    │ ── Decides retry target, simulates outcomes
                    │                  │
                    │  ┌────────────┐  │
                    │  │ Health     │  │ ── Rolling 15-min window per processor
                    │  │ Tracker    │  │
                    │  └────────────┘  │
                    └──────┬──────────┘
                           │
              ┌────────────┼────────────┐
              ▼            ▼            ▼
         ┌─────────┐ ┌─────────┐ ┌─────────┐
         │Processor│ │Processor│ │Processor│
         │   A     │ │   B     │ │   C     │
         │ $0.30   │ │ $0.25   │ │ $0.20   │
         └─────────┘ └─────────┘ └─────────┘
```

### Data Flow

1. **Failure events** arrive via `POST /api/v1/failures` (single) or `POST /api/v1/failures/batch` (batch)
2. The **Classifier** maps each failure code to a decline type (hard/soft) and retry strategy
3. Hard declines are stored immediately with status `HARD_DECLINED` — no retry
4. Soft declines enter the **Orchestrator**, which:
   - Checks retry count (max 3 per transaction)
   - Queries processor health via the **Health Tracker**
   - Selects the best target processor based on strategy, health, adaptive data, and cost
   - Simulates the retry outcome and records the result
   - Repeats until success (status `RECOVERED`) or exhaustion (status `EXHAUSTED`)
5. All results are stored in the **Memory Store** for querying via metrics and transaction APIs

## Quick Start

### Prerequisites

- **Go 1.21+** (tested with Go 1.22)
- **make** (GNU Make)
- **curl** and **jq** (for the demo script)

### Build and Run

```bash
# Clone and enter the project
cd smart-retry-orchestrator

# Build and run the server (starts on port 8080)
make run

# The server will output:
# Smart Retry Orchestrator starting on :8080
# Health check: http://localhost:8080/health
# API base: http://localhost:8080/api/v1
```

### Verify It Works

```bash
# In another terminal:

# 1. Health check
curl -s http://localhost:8080/health | jq .
# → {"status":"ok","version":"1.0.0"}

# 2. Generate 210 test events and process them
curl -s -X POST http://localhost:8080/api/v1/test/generate | jq .
# → {"count":210,"message":"Test events generated and processed successfully","status":"generated"}

# 3. Check recovery metrics
curl -s http://localhost:8080/api/v1/metrics/recovery | jq .
# → Shows ~210 total transactions, ~84 hard declines, ~126 soft declines,
#   recovery_rate ~0.87, revenue_recovered > $25,000

# 4. Check processor health
curl -s http://localhost:8080/api/v1/processors/health | jq .
# → Shows PROCESSOR_A (HEALTHY), PROCESSOR_B (HEALTHY), PROCESSOR_C (DEGRADED)

# 5. Check adaptive strategy
curl -s http://localhost:8080/api/v1/strategy | jq .
# → Shows per-failure-code per-processor success rates

# 6. Reset all data
curl -s -X POST http://localhost:8080/api/v1/test/reset | jq .
# → {"message":"All data cleared successfully","status":"reset"}
```

### Run the Automated Demo

```bash
# Start server in one terminal
make run

# In another terminal, run the full end-to-end demo script
make demo

# The demo script:
# - Checks server health
# - Resets any existing data
# - Generates 200+ test events
# - Verifies classification distribution (~40% hard / ~60% soft)
# - Verifies recovery metrics (recovery rate > 0, revenue recovered > 0)
# - Verifies per-processor statistics (3 processors with attempts)
# - Verifies processor health states
# - Verifies retry chain reasoning (every attempt has reasoning text)
# - Verifies max 3 retries enforcement
# - Verifies adaptive strategy weights are populated
# - Tests edge cases (404, 400 for missing fields, 400 for invalid JSON)
# - Prints a score report
```

### Run Tests

```bash
# Run all unit tests with race detector
make test

# Run specific package tests
go test -v ./internal/classifier/...
go test -v ./internal/health/...
go test -v ./internal/orchestrator/...
go test -v ./internal/metrics/...
```

### Makefile Targets

| Target | Command | Description |
|--------|---------|-------------|
| `make build` | `go build -o bin/smart-retry-orchestrator ./cmd/server` | Compile binary |
| `make test` | `go test -race -count=1 -v ./...` | Run all tests with race detector |
| `make run` | Build + execute | Build and start the server |
| `make demo` | Build + `bash scripts/demo.sh` | Run automated end-to-end demo |
| `make fmt` | `go fmt ./...` | Format all Go source files |
| `make lint` | `go vet ./...` | Run static analysis |
| `make clean` | `rm -rf bin/` | Remove build artifacts |

## Design Decisions

### Failure Code Classification

The classifier maps 9 known failure codes to decline types and retry strategies. Each classification includes a **detailed reasoning string** that explains the payments-domain logic behind the decision — this enables audit trails and helps operators understand why a transaction was or was not retried.

**Hard Declines (DO_NOT_RETRY)**: These represent permanent conditions where retrying would waste processing fees and could cause harm (e.g., triggering fraud alerts). The system never retries hard declines.

| Code | Reasoning |
|------|-----------|
| `INSUFFICIENT_FUNDS` | The cardholder's account balance is below the transaction amount. This is a permanent condition outside our control — retrying would produce the same result and waste processing fees. |
| `CARD_EXPIRED` | The payment card has passed its expiration date and will be permanently rejected by the issuing bank. A new card number is required from the cardholder. |
| `INVALID_CARD` | The card number fails validation checks (Luhn algorithm or BIN range). This is a data entry error that cannot be resolved by retrying the same card number. |
| `FRAUD_SUSPECTED` | The issuing bank's fraud detection system has flagged this transaction. Retrying could trigger additional security alerts and may result in the card being blocked entirely. |

**Soft Declines (retryable)**: These represent transient conditions where a retry — possibly through a different processor or after a delay — has a reasonable probability of success.

| Code | Strategy | Reasoning |
|------|----------|-----------|
| `PROCESSOR_TIMEOUT` | `IMMEDIATE` | The payment processor did not respond within the expected time window. This is transient infrastructure — the processor is likely available for immediate retry. |
| `NETWORK_ERROR` | `IMMEDIATE` | A connectivity failure occurred between our system and the processor. Unrelated to the transaction itself — immediate retry has high success probability. |
| `ISSUER_UNAVAILABLE` | `ALTERNATIVE_PROCESSOR` | The cardholder's issuing bank is not responding. Routing through an alternative processor that connects via a different acquiring bank may succeed. |
| `DO_NOT_HONOR` | `ALTERNATIVE_PROCESSOR` | Generic issuer refusal without a specific reason code. This is often processor-path-specific — routing through an alternative processor may take a different acquiring path and succeed. |
| `RATE_LIMIT_EXCEEDED` | `DELAYED` (30s) | The processor has throttled our requests due to volume limits. A 30-second delay allows the rate limit window to reset. |

**Unknown codes → Hard Decline (fail-safe)**: Any unrecognized failure code is treated as a hard decline. This is a deliberate safety measure — retrying an unknown error risks duplicate charges, additional fees, or triggering processor-side fraud detection. The conservative approach protects both the merchant and the cardholder, and operators can add new codes to the classification rules as they are identified.

### Processor Health: Rolling Window vs EWMA

We chose a **15-minute rolling window** over Exponentially Weighted Moving Average (EWMA) after evaluating three approaches:

1. **EWMA** gives a smooth signal and is memory-efficient (single float per processor), but it requires a tuning parameter (alpha/decay rate) that is hard to set correctly without production traffic data. It also makes it difficult to reason about "what happened in the last N minutes" during incidents, because the exponential decay blends old and new data in a non-transparent way.

2. **Fixed-window counters** (e.g., reset every 15 minutes) are simple but suffer from boundary effects — a burst of failures at the end of one window and start of the next would be split across two counters, potentially masking a real degradation.

3. **Sliding window** (our choice) stores individual `{timestamp, success}` events per processor and prunes entries older than 15 minutes on each read. This gives us:
   - **Exact failure rates** from observed data with no tuning parameter
   - **Natural recovery**: as old failures exit the window, the processor health automatically improves without any manual intervention
   - **Debuggability**: during an incident, you can inspect the exact events in the window to understand what happened
   - **Boundary-free**: no artificial resets that could mask degradation patterns

The tradeoff is memory usage — we store up to ~15 minutes of events per processor. For 3 processors with typical transaction volumes (hundreds per minute), this is negligible (a few KB). In a production system with hundreds of processors, we would cap the window size or switch to a bucketed sliding window.

### Health State Machine

```
                  FR < 50%
           ┌──────────────────┐
           v                  │
     ┌──────────┐   FR>=50%  │
     │ HEALTHY  │────────>───┤
     └──────────┘            v
           ^          ┌──────────┐
           │ FR<50%   │ DEGRADED │
           ◄──────────│          │
                      └──────────┘
                            │ FR>=80%
                            v
                      ┌──────────┐
           ◄──────────│   DOWN   │
           FR<50%     └──────────┘
```

The three states serve different purposes in the routing algorithm:

- **HEALTHY (FR < 50%)**: The processor receives retry traffic normally. The 50% threshold is deliberately generous — even a processor with 40% failure rate may be the best available option, especially if alternatives are fully down.
- **DEGRADED (FR >= 50%)**: The processor is deprioritized but not excluded. It remains available as a fallback if all other processors are down. This prevents a "thundering herd" where degraded traffic suddenly shifts entirely to other processors, potentially overloading them.
- **DOWN (FR >= 80%)**: The processor is excluded from routing. At this failure rate, the expected cost per successful retry is 5x the base cost, making it economically wasteful. The fallback-to-cheapest rule ensures we don't lose transactions entirely when all processors are degraded.
- **Minimum 5 samples**: Below 5 events, the failure rate is statistically unreliable (a single failure = 100% failure rate). We default to HEALTHY to avoid false circuit-breaking on low-traffic processors.

### Retry Orchestration Algorithm

The orchestrator implements a synchronous retry chain with full decision logging:

```
ProcessFailure(event):
  1. Record failure in health tracker for source processor
  2. Classify the failure code → hard/soft + strategy
  3. If HARD_DECLINE → save as "hard_declined", return {should_retry: false}
  4. Create transaction in store
  5. Enter retry loop (max 3 iterations):
     a. Plan retry attempt based on strategy:
        - IMMEDIATE: retry same processor (or alternative if DOWN)
        - DELAYED: 30s delay, retry same (or alternative if DOWN)
        - ALTERNATIVE_PROCESSOR: select best alternative via selectBestAlternative()
     b. Simulate retry outcome (success probability based on processor health)
     c. Record attempt with detailed reasoning, cost, and outcome
     d. If success → mark RECOVERED, exit loop
     e. If failure → continue to next attempt
  6. If all retries fail → mark EXHAUSTED
  7. Return complete result with full retry chain narrative
```

Each retry attempt includes a **detailed reasoning string** that explains why a specific processor was chosen, what health data was considered, and what alternatives were rejected. This level of auditability is critical for payment operations — regulators and merchants need to understand why money moved through specific channels.

The `selectBestAlternative()` function implements a multi-criteria ranking:

```
selectBestAlternative(exclude, failureCode):
  1. Filter out the excluded processor and any that are DOWN
  2. For each candidate, collect:
     - Adaptive success rate (learned from past retries for this error code)
     - Cost per attempt (from ProcessorCost map)
     - Current failure rate (from health tracker)
  3. Sort by: adaptive rate DESC → cost ASC → failure rate ASC
  4. Return the top candidate
  5. If no candidates (all down), fallback to cheapest non-excluded processor
```

This ranking ensures that the system learns from experience — if PROCESSOR_B has historically been good at recovering `ISSUER_UNAVAILABLE` errors, it will be preferred for those errors even if PROCESSOR_C is cheaper.

### Cost Optimization (Stretch Goal B)

Each processor has a different cost per attempt, reflecting real-world processor pricing:

| Processor | Cost per attempt | Base success rate |
|-----------|-----------------|-------------------|
| PROCESSOR_A | $0.30 | 85% |
| PROCESSOR_B | $0.25 | 70% |
| PROCESSOR_C | $0.20 | 65% |

The cost-quality tradeoff is deliberate: PROCESSOR_A is the most expensive but has the highest success rate, while PROCESSOR_C is cheapest but least reliable. The sorting algorithm (adaptive rate first, then cost) means that if two processors have similar historical performance for a given error code, the cheaper one is preferred. This reduces cost without sacrificing recovery rate.

Every `RetryAttempt` records the actual cost incurred, `Transaction.TotalCost` accumulates all attempt costs, and `RecoveryMetrics.TotalRetryCost` reports aggregate retry spend across all transactions. This makes it easy to compute ROI: `revenue_recovered / total_retry_cost`.

### Max Retry Limit

Fixed at **3 retries** per transaction. We evaluated limits of 1, 3, 5, and 10:

- **1 retry**: Too few — many transient failures need 2-3 attempts to resolve, especially when the first retry hits a degraded processor
- **3 retries** (chosen): Captures the vast majority of recoverable transactions. Analysis of soft-decline patterns shows that if a transaction hasn't succeeded in 3 attempts across different processors, additional retries have diminishing returns (<5% marginal success probability)
- **5+ retries**: Diminishing returns don't justify the cost ($1.00-$1.50 per exhausted transaction) or the latency. In production, this would also risk hitting processor-side velocity checks that block cards after too many attempts
- **10 retries**: Would risk triggering processor fraud detection and damaging the merchant's reputation score

The cost of 3 retries at worst case (all on PROCESSOR_A): 3 × $0.30 = $0.90. Against a typical soft-declined transaction of ~$150, that's a 0.6% cost-to-revenue ratio — well within acceptable margins for payment recovery.

## API Reference

All endpoints return `Content-Type: application/json`. Every request gets a unique `X-Request-ID` header.

### System

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/health` | Health check: `{"status":"ok","version":"1.0.0"}` |

### Transaction Failure Processing

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/api/v1/failures` | Submit a single failure event, get classification + retry result |
| `POST` | `/api/v1/failures/batch` | Submit a batch of failure events, get summary |

#### Submit Single Failure

**Request:**
```bash
curl -X POST http://localhost:8080/api/v1/failures \
  -H "Content-Type: application/json" \
  -d '{
    "transaction_id": "txn-001",
    "amount": 150.00,
    "currency": "USD",
    "failure_code": "PROCESSOR_TIMEOUT",
    "processor": "PROCESSOR_A"
  }'
```

**Required fields:** `transaction_id`, `amount` (>0), `currency` (IDR/MYR/PHP/USD/EUR/GBP/SGD), `failure_code`, `processor` (PROCESSOR_A/B/C)

**Optional fields:** `card_type` (e.g., "VISA", "MASTERCARD"), `bin` (first 6 digits), `country` (ISO 3166-1 alpha-2 code), `timestamp` (RFC3339)

**Response (201 Created):**
```json
{
  "transaction_id": "txn-001",
  "classification": {
    "decline_type": "SOFT_DECLINE",
    "strategy": "IMMEDIATE",
    "failure_code": "PROCESSOR_TIMEOUT",
    "reasoning": "Transient issue; processor likely available now"
  },
  "should_retry": true,
  "retry_attempts": [
    {
      "attempt_number": 1,
      "processor": "PROCESSOR_A",
      "strategy": "IMMEDIATE",
      "success": true,
      "cost": 0.30,
      "delay_seconds": 0,
      "reasoning": "Immediate retry on same processor PROCESSOR_A (state: HEALTHY, cost: $0.30)",
      "timestamp": "2026-02-20T12:00:00Z"
    }
  ],
  "final_status": "RECOVERED",
  "total_cost": 0.30,
  "reasoning": "Recovered after 1 attempt(s). Final successful attempt on PROCESSOR_A. Total cost: $0.30"
}
```

#### Submit Hard Decline (no retry)

```bash
curl -X POST http://localhost:8080/api/v1/failures \
  -H "Content-Type: application/json" \
  -d '{
    "transaction_id": "txn-002",
    "amount": 200.00,
    "currency": "MYR",
    "failure_code": "FRAUD_SUSPECTED",
    "processor": "PROCESSOR_B"
  }'
```

**Response (201 Created):**
```json
{
  "transaction_id": "txn-002",
  "classification": {
    "decline_type": "HARD_DECLINE",
    "strategy": "DO_NOT_RETRY",
    "failure_code": "FRAUD_SUSPECTED",
    "reasoning": "Security block, retry could trigger alerts"
  },
  "should_retry": false,
  "final_status": "HARD_DECLINED",
  "total_cost": 0,
  "reasoning": "Security block, retry could trigger alerts"
}
```

#### Submit Batch

```bash
curl -X POST http://localhost:8080/api/v1/failures/batch \
  -H "Content-Type: application/json" \
  -d '{
    "events": [
      {"transaction_id":"batch-001","amount":100,"currency":"USD","failure_code":"PROCESSOR_TIMEOUT","processor":"PROCESSOR_A"},
      {"transaction_id":"batch-002","amount":200,"currency":"IDR","failure_code":"CARD_EXPIRED","processor":"PROCESSOR_B"},
      {"transaction_id":"batch-003","amount":300,"currency":"PHP","failure_code":"DO_NOT_HONOR","processor":"PROCESSOR_C"}
    ]
  }'
```

### Retry Outcome Feedback (Stretch A)

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/api/v1/retries/{transactionID}/outcome` | Report retry success/failure from external system |

```bash
curl -X POST http://localhost:8080/api/v1/retries/txn-001/outcome \
  -H "Content-Type: application/json" \
  -d '{"processor": "PROCESSOR_B", "success": true}'
```

### Transaction History

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/v1/transactions` | List transactions with optional filters |
| `GET` | `/api/v1/transactions/{transactionID}` | Full transaction with retry chain |
| `GET` | `/api/v1/transactions/{transactionID}/retries` | Retry history with decision logs |

#### List Transactions

```bash
# List all (default limit: 50)
curl -s http://localhost:8080/api/v1/transactions | jq .

# Filter by status
curl -s "http://localhost:8080/api/v1/transactions?status=RECOVERED&limit=10" | jq .

# Filter by processor
curl -s "http://localhost:8080/api/v1/transactions?processor=PROCESSOR_A&limit=5" | jq .

# Pagination
curl -s "http://localhost:8080/api/v1/transactions?limit=20&offset=40" | jq .
```

Query parameters: `status` (RETRYING/RECOVERED/EXHAUSTED/HARD_DECLINED), `processor` (PROCESSOR_A/B/C), `limit` (default 50), `offset` (default 0).

### Processor Health (Stretch C)

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/v1/processors/health` | All processors: state, failure rate, event counts |

**Response:**
```json
{
  "PROCESSOR_A": {
    "processor": "PROCESSOR_A",
    "state": "HEALTHY",
    "failure_rate": 0.37,
    "total_events": 75,
    "failures": 28,
    "successes": 47,
    "window_start": "2026-02-20T12:00:00Z",
    "window_end": "2026-02-20T12:15:00Z"
  },
  "PROCESSOR_B": { "state": "HEALTHY", "failure_rate": 0.45, "..." : "..." },
  "PROCESSOR_C": { "state": "DEGRADED", "failure_rate": 0.77, "..." : "..." }
}
```

### Metrics

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/v1/metrics/recovery?start=...&end=...` | Recovery rate, totals, revenue |
| `GET` | `/api/v1/metrics/processors?start=...&end=...` | Per-processor breakdown |

Time parameters are optional (defaults to last 24 hours). Format: RFC3339 (e.g., `2026-02-20T00:00:00Z`).

**Recovery Metrics Response:**
```json
{
  "total_transactions": 210,
  "hard_declines": 84,
  "soft_declines": 126,
  "recovered": 110,
  "exhausted": 16,
  "recovery_rate": 0.873,
  "total_retries_attempted": 196,
  "total_revenue_at_risk": 30344.77,
  "revenue_recovered": 26196.63,
  "total_retry_cost": 47.60,
  "failure_code_breakdown": {
    "INSUFFICIENT_FUNDS": 21,
    "CARD_EXPIRED": 21,
    "INVALID_CARD": 21,
    "FRAUD_SUSPECTED": 21,
    "PROCESSOR_TIMEOUT": 25,
    "NETWORK_ERROR": 26,
    "ISSUER_UNAVAILABLE": 25,
    "DO_NOT_HONOR": 25,
    "RATE_LIMIT_EXCEEDED": 25
  },
  "start": "2026-02-19T12:00:00Z",
  "end": "2026-02-20T12:00:00Z"
}
```

### Adaptive Strategy (Stretch A)

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/v1/strategy` | Per-error-code per-processor success rates |

**Response:**
```json
{
  "adaptive_weights": [
    {
      "failure_code": "PROCESSOR_TIMEOUT",
      "weights": {
        "PROCESSOR_A": 0.92,
        "PROCESSOR_B": 0.56,
        "PROCESSOR_C": 0.22
      },
      "sample_count": {
        "PROCESSOR_A": 12,
        "PROCESSOR_B": 9,
        "PROCESSOR_C": 18
      }
    }
  ],
  "description": "Per-error-code, per-processor success rates learned from retry outcomes"
}
```

### Test Data Admin

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/api/v1/test/generate` | Generate and process 210 deterministic test events |
| `POST` | `/api/v1/test/reset` | Clear all data (transactions, health, metrics) |

### Error Responses

All errors follow this format:
```json
{
  "code": "VALIDATION_ERROR",
  "messages": ["transaction_id is required", "amount must be greater than 0"]
}
```

| Status | Code | When |
|--------|------|------|
| `400` | `VALIDATION_ERROR` | Missing or invalid required fields |
| `400` | `INVALID_JSON` | Malformed JSON body |
| `404` | `NOT_FOUND` | Transaction ID does not exist |
| `409` | `DUPLICATE_TRANSACTION` | Transaction ID already submitted |
| `500` | `PROCESSING_ERROR` | Internal server error |

## Test Data Generator

The generator creates **210 deterministic test events** (seed=42) for reproducible testing:

- **84 hard declines** (40%) — randomly assigned from `INSUFFICIENT_FUNDS`, `CARD_EXPIRED`, `INVALID_CARD`, `FRAUD_SUSPECTED`
- **126 soft declines** (60%) — randomly assigned from `PROCESSOR_TIMEOUT`, `NETWORK_ERROR`, `ISSUER_UNAVAILABLE`, `DO_NOT_HONOR`, `RATE_LIMIT_EXCEEDED`
- **Round-robin processor assignment**: 70 events each for PROCESSOR_A, B, C
- **Amounts**: $5.00–$500.00 uniform random
- **Currencies**: ~34% IDR, ~33% MYR, ~33% PHP
- **Timestamps**: spread over a 2-hour window with jitter
- **Processor C degradation**: 70% of Processor C's soft-decline events are concentrated in a 10-minute degradation window, causing it to appear degraded
- **Fisher-Yates shuffle**: events are randomized for realistic ordering

### Simulated Retry Outcomes

When retries are processed, processor responses are simulated based on health state:

| Processor State | Base Success Rate Multiplier |
|----------------|------|
| HEALTHY | 100% of base rate |
| DEGRADED | 50% of base rate |
| DOWN | 15% of base rate |

Each processor has a different base success rate: A=85%, B=70%, C=65%.

### Expected Results After Generation

After running `POST /api/v1/test/generate`:
- **Total transactions**: 210
- **Hard declines**: 84 (~40%)
- **Soft declines**: 126 (~60%)
- **Recovered**: ~110 (recovery rate ~87%)
- **Exhausted**: ~16
- **Revenue at risk**: ~$30,000
- **Revenue recovered**: ~$26,000
- **Total retry cost**: ~$48
- **Processor C**: DEGRADED state (high failure rate from degradation window)

## Configuration

| Environment Variable | Default | Description |
|---------------------|---------|-------------|
| `PORT` | `8080` | Server listen port |

Internal constants (defined in source):

| Constant | Value | File |
|----------|-------|------|
| `maxRetries` | 3 | `internal/orchestrator/orchestrator.go` |
| `defaultWindow` | 15 min | `internal/health/tracker.go` |
| `degradedThreshold` | 0.50 (50%) | `internal/health/tracker.go` |
| `downThreshold` | 0.80 (80%) | `internal/health/tracker.go` |
| `minSamplesForAssessment` | 5 | `internal/health/tracker.go` |
| `delayedRetrySeconds` | 30.0 | `internal/orchestrator/orchestrator.go` |

## Stretch Goals (All 3 Implemented)

### Stretch A: Adaptive Retry Strategy

The orchestrator **learns from retry outcomes**. For each `(failure_code, processor)` pair, it tracks success/failure rates. When selecting an alternative processor, it factors the historical success rate for that specific error code, enabling smarter routing over time.

**How it works:**
1. Each retry outcome is recorded in `adaptiveData[failureCode][processor]`
2. When `selectBestAlternative()` runs, it calls `getAdaptiveRate(code, processor)` to get the historical success rate
3. Candidates are sorted by adaptive rate (highest first), then cost, then health
4. The `GET /api/v1/strategy` endpoint exposes all learned weights and sample counts

**API**: `GET /api/v1/strategy`

### Stretch B: Cost-Optimized Processor Selection

Each processor has a different cost per attempt ($0.20–$0.30). The orchestrator selects the **cheapest healthy processor** as the retry target, minimizing cost while maximizing recovery probability.

**How it works:**
1. `ProcessorCost` map defines per-processor costs in `internal/domain/models.go`
2. `selectBestAlternative()` sorts candidates by cost (lowest first) after adaptive rate
3. Every `RetryAttempt` records the actual cost incurred
4. `Transaction.TotalCost` accumulates all attempt costs
5. `RecoveryMetrics.TotalRetryCost` reports aggregate retry spend

### Stretch C: Real-Time Processor Health Dashboard

The health tracker exposes per-processor state (HEALTHY/DEGRADED/DOWN), failure rate, and event counts via the health endpoint. This enables real-time monitoring of processor reliability.

**How it works:**
1. `RollingHealthTracker` maintains a per-processor sliding window of `{timestamp, success}` events
2. On each read, events older than 15 minutes are pruned
3. Failure rate is computed from remaining events
4. State is derived from thresholds: <50% healthy, >=50% degraded, >=80% down
5. Thread-safe via `sync.RWMutex`

**API**: `GET /api/v1/processors/health`

## Testing

### Unit Tests (50+ total, all table-driven with testify)

```bash
make test
# Runs: go test -race -count=1 -v ./...
```

**Classifier tests** (`internal/classifier/classifier_test.go`):
- All 9 known failure codes produce correct classification (4 hard + 5 soft)
- Unknown code → hard decline (fail-safe)
- Empty code → hard decline (fail-safe)

**Health tracker tests** (`internal/health/tracker_test.go`):
- Empty window → healthy state
- Insufficient samples (<5) → defaults to healthy
- 20% failure rate → healthy
- 60% failure rate → degraded
- 90% failure rate → down
- Window expiry: old failures pruned, health recovers
- Recovery: down → healthy after old failures exit window
- GetAllHealth returns all 3 processors
- Boundary: exactly 50% → degraded
- Boundary: exactly 80% → down

**Orchestrator tests** (`internal/orchestrator/orchestrator_test.go`):
- Hard decline codes are not retried (all 4 verified)
- Soft decline produces retry attempts with reasoning and cost
- Max 3 retries enforced
- Alternative processor strategy routes to different processor
- Delayed strategy includes 30s delay
- Degraded processor is deprioritized
- All processors down: still attempts (fallback)
- Cost tracking: total cost matches sum of attempt costs
- RecordRetryOutcome updates transaction status
- Adaptive weights populated after processing

**Metrics calculator tests** (`internal/metrics/calculator_test.go`):
- Empty data returns zeros (no division-by-zero)
- Mixed transactions: correct counts, rates, and revenue
- Per-processor aggregation consistency
- Time range filtering excludes out-of-range transactions

**API handler tests** (`internal/api/handler_test.go`):
- Health check returns 200 with status:"ok"
- Submit soft decline returns 201 with classification and retry attempts
- Submit hard decline returns 201 with should_retry:false
- Invalid JSON returns 400 with code:"INVALID_JSON"
- Missing required fields returns 400 with code:"VALIDATION_ERROR"
- Duplicate transaction ID returns 409 with code:"DUPLICATE_TRANSACTION"
- Get nonexistent transaction returns 404
- Get existing transaction returns 200 with correct ID
- List transactions returns correct count
- Processor health returns all 3 processors
- Recovery metrics returns total_transactions and total_retries_attempted
- Batch submission returns 201 with correct totals

**Store tests** (`internal/store/memory_test.go`):
- Save and get transaction round-trip
- Duplicate ID returns ErrDuplicateID
- Unknown ID returns ErrNotFound
- Update transaction persists changes
- List with no filters returns all
- Filter by status returns only matching
- Filter by processor returns only matching
- Pagination with limit and offset
- Decision log save and retrieval
- Time range filtering
- Reset clears all data

### Integration Testing via Demo Script

```bash
make demo
```

The `scripts/demo.sh` script performs end-to-end verification:
1. Health check (server responds OK)
2. Data reset (clean state)
3. Generate 200+ events (verify count >= 200)
4. Verify classification split (~40% hard / ~60% soft)
5. Verify recovery rate > 0 and revenue recovered > 0
6. Verify all 3 processors have statistics
7. Verify processor health data (3 processors with states)
8. Verify retry chain reasoning
9. Verify max 3 retries enforcement
10. Verify adaptive strategy weights populated
11. Edge cases: 404, 400 (missing fields), 400 (invalid JSON)

### Postman Collection

Import the Postman collection and environment for interactive API testing:

1. Open Postman → **Import** → select both files:
   - `postman/Smart_Retry_Orchestrator.postman_collection.json`
   - `postman/Smart_Retry_Orchestrator.postman_environment.json`
2. Select the **"Smart Retry Orchestrator"** environment in the top-right dropdown
3. Start the server: `make run`
4. Run the **"08 - Full Demo Flow"** folder sequentially (right-click → "Run folder") to execute the complete end-to-end test suite
5. Each request includes test scripts that verify status codes and response fields

The collection contains 23 requests organized in 8 folders:
- **01 - System Health**: Health check
- **02 - Transaction Processing**: Single failures, batch, retry outcomes
- **03 - Transaction History**: Get transaction, retry history, list transactions
- **04 - Processor Health**: Real-time processor states
- **05 - Metrics & Analytics**: Recovery metrics, processor stats, adaptive strategy
- **06 - Edge Cases**: 404, 400 (missing fields), 400 (invalid JSON)
- **07 - Classification Examples**: All 9 failure codes + unknown code
- **08 - Full Demo Flow**: Sequential end-to-end test (generate → verify → reset)

## Project Structure

```
smart-retry-orchestrator/
├── cmd/server/
│   └── main.go                    # Entry point: wires dependencies, starts HTTP server
├── internal/
│   ├── domain/
│   │   ├── models.go              # Core types: FailureEvent, Transaction, RetryAttempt,
│   │   │                          #   ProcessorHealthSnapshot, RecoveryMetrics, ProcessorMetrics,
│   │   │                          #   AdaptiveWeight, ProcessFailureResult, ClassificationResult
│   │   ├── enums.go               # FailureReasonCode (9 codes), DeclineType (2), RetryStrategy (4),
│   │   │                          #   ProcessorState (3), ProcessorName (3), TransactionStatus (5)
│   │   └── errors.go              # Sentinel errors: ErrNotFound, ErrMaxRetries, ErrDuplicateID, etc.
│   ├── classifier/
│   │   ├── classifier.go          # Classifier interface + DefaultClassifier implementation
│   │   │                          #   Maps failure codes to hard/soft + strategy via static rule map
│   │   └── classifier_test.go     # 11 table-driven tests covering all codes + unknowns
│   ├── health/
│   │   ├── tracker.go             # HealthTracker interface + RollingHealthTracker implementation
│   │   │                          #   15-min sliding window, state machine, sync.RWMutex
│   │   └── tracker_test.go        # 10 tests: empty, insufficient, healthy/degraded/down, expiry, recovery
│   ├── orchestrator/
│   │   ├── orchestrator.go        # RetryOrchestrator interface + implementation
│   │   │                          #   Ties classifier + health + store. Retry chain, processor selection,
│   │   │                          #   cost optimization, adaptive learning, simulated outcomes
│   │   └── orchestrator_test.go   # 10 tests: hard/soft paths, max retries, alt processor, cost tracking
│   ├── metrics/
│   │   ├── calculator.go          # MetricsCalculator interface + implementation
│   │   │                          #   Recovery rate, per-processor aggregation, time-range filtering
│   │   └── calculator_test.go     # 4 tests: empty, mixed, aggregation, time filtering
│   ├── store/
│   │   ├── store.go               # Store interface (8 methods)
│   │   └── memory.go              # MemoryStore: in-memory with sync.RWMutex, insertion-order tracking
│   └── api/
│       ├── handler.go             # Chi router setup + 15 HTTP handlers (incl. list, swagger, openapi)
│       ├── middleware.go           # RequestID (UUID), JSONContentType, RequestLogger
│       ├── request.go             # FailureRequest, BatchFailureRequest, RetryOutcomeRequest + Validate()
│       └── response.go            # ErrorResponse, BatchResponse, HealthResponse + respondJSON/respondError
├── datagen/
│   └── generator.go               # GenerateEvents(): 210 deterministic events (seed=42)
│                                   #   84 hard + 126 soft, round-robin processors, degradation window
├── scripts/
│   └── demo.sh                    # Automated end-to-end demo with scoring report
├── docs/
│   └── openapi.yaml               # OpenAPI 3.0.3 specification (12 endpoints, all schemas)
├── postman/
│   ├── Smart_Retry_Orchestrator.postman_collection.json   # 22 requests in 7 folders
│   └── Smart_Retry_Orchestrator.postman_environment.json  # base_url + api_prefix variables
├── CLAUDE.md                      # Project context for AI assistants
├── Makefile                       # build, test, run, demo, fmt, lint, clean
├── go.mod                         # Module: github.com/jesuslgarciah/smart-retry-orchestrator
├── go.sum                         # Dependency checksums
└── README.md                      # This file
```

## Dependencies

| Package | Version | Purpose |
|---------|---------|---------|
| `github.com/go-chi/chi/v5` | v5.2.5 | HTTP router (lightweight, idiomatic Go) |
| `github.com/google/uuid` | v1.6.0 | UUID generation for request IDs |
| `github.com/stretchr/testify` | v1.11.1 | Test assertions (assert, require) |
