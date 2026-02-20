# Smart Retry Orchestrator

## Project Overview

A standalone Go backend service that classifies payment failures (hard vs soft), intelligently retries soft declines across 3 processors based on health, and exposes metrics APIs to prove revenue recovery.

Built for TigerPay to recover ~$880K/month lost to unretried soft declines.

## How to Build, Run, and Test

```bash
# Build the binary
make build

# Run all unit tests with race detector (25 tests across 4 packages)
make test

# Build and start the server on port 8080
make run

# Run the automated end-to-end demo script (requires server running)
make demo

# Or manually:
go build -o bin/smart-retry-orchestrator ./cmd/server
PORT=8080 ./bin/smart-retry-orchestrator
```

## Quick Verification Steps

1. Start server: `make run`
2. Health check: `curl http://localhost:8080/health` → `{"status":"ok","version":"1.0.0"}`
3. Generate test data: `curl -X POST http://localhost:8080/api/v1/test/generate` → `{"count":210,...}`
4. Check metrics: `curl http://localhost:8080/api/v1/metrics/recovery` → recovery_rate ~0.87
5. Check health: `curl http://localhost:8080/api/v1/processors/health` → 3 processors with states
6. Check strategy: `curl http://localhost:8080/api/v1/strategy` → adaptive weights per error code
7. Reset: `curl -X POST http://localhost:8080/api/v1/test/reset` → clears all data

## Tech Stack

- **Language**: Go 1.21+
- **Router**: Chi v5 (`github.com/go-chi/chi/v5`)
- **Testing**: testify/assert + testify/require (`github.com/stretchr/testify`)
- **UUIDs**: `github.com/google/uuid` (for request IDs)
- **Storage**: In-memory with sync.RWMutex
- **Build**: Makefile

## Architecture

```
POST /api/v1/failures → API Layer (Chi) → Classifier → Orchestrator → Health Tracker
                                                ↓                          ↓
                                          Memory Store            Processor Pool (A/B/C)
```

### Key Packages

| Package | Purpose | Key Interface |
|---------|---------|---------------|
| `internal/domain` | Models, enums, errors (zero deps) | N/A |
| `internal/classifier` | Maps failure codes → hard/soft + retry strategy | `Classifier` |
| `internal/health` | Rolling 15-min window processor health tracker | `HealthTracker` |
| `internal/orchestrator` | Retry decision engine with cost optimization + adaptive learning | `RetryOrchestrator` |
| `internal/metrics` | Recovery rate and per-processor aggregation | `MetricsCalculator` |
| `internal/store` | Store interface + in-memory implementation | `Store` |
| `internal/api` | HTTP handlers, middleware, request/response DTOs | N/A |
| `datagen` | Deterministic 210-event generator (seed=42) | N/A |

## API Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/health` | Health check |
| `GET` | `/docs` | Swagger UI (interactive API documentation) |
| `GET` | `/docs/openapi.yaml` | OpenAPI 3.0 spec file |
| `POST` | `/api/v1/failures` | Submit single failure, returns classification + retry result |
| `POST` | `/api/v1/failures/batch` | Submit batch of failures |
| `POST` | `/api/v1/retries/{transactionID}/outcome` | Report retry outcome (Stretch A) |
| `GET` | `/api/v1/transactions` | List transactions with filters (?status=&processor=&limit=&offset=) |
| `GET` | `/api/v1/transactions/{transactionID}` | Get transaction with retry chain |
| `GET` | `/api/v1/transactions/{transactionID}/retries` | Get retry history + decision logs |
| `GET` | `/api/v1/processors/health` | Processor health status (Stretch C) |
| `GET` | `/api/v1/metrics/recovery?start=&end=` | Recovery rate, revenue, failure_code_breakdown, total_retries_attempted |
| `GET` | `/api/v1/metrics/processors?start=&end=` | Per-processor breakdown |
| `GET` | `/api/v1/strategy` | Adaptive strategy weights (Stretch A) |
| `POST` | `/api/v1/test/generate` | Generate and process 210 deterministic test events (auto-resets) |
| `POST` | `/api/v1/test/reset` | Clear all data |

## Classification Rules

| Code | Decline Type | Strategy |
|------|-------------|----------|
| `INSUFFICIENT_FUNDS` | HARD | DO_NOT_RETRY |
| `CARD_EXPIRED` | HARD | DO_NOT_RETRY |
| `INVALID_CARD` | HARD | DO_NOT_RETRY |
| `FRAUD_SUSPECTED` | HARD | DO_NOT_RETRY |
| `PROCESSOR_TIMEOUT` | SOFT | IMMEDIATE |
| `NETWORK_ERROR` | SOFT | IMMEDIATE |
| `ISSUER_UNAVAILABLE` | SOFT | ALTERNATIVE_PROCESSOR |
| `DO_NOT_HONOR` | SOFT | ALTERNATIVE_PROCESSOR |
| `RATE_LIMIT_EXCEEDED` | SOFT | DELAYED (30s) |
| Unknown | HARD | DO_NOT_RETRY (fail-safe) |

## Key Design Decisions

- **Unknown failure codes → hard decline**: Fail-safe; never retry unknowns to prevent duplicate charges
- **Rolling 15-min window** for health: Deterministic, auto-recovering, debuggable (chosen over EWMA)
- **Max 3 retries**: Balances recovery (~87% rate) vs cost ($0.60-$0.90 worst case) vs latency
- **FailureEvent includes**: CardType, BIN, Country fields for anonymized cardholder data
- **Processor profiles**: A=healthy (~10% failure), B=moderate (~25%), C=severe degradation window (~70%)
- **Processor costs**: A=$0.30 (85% success), B=$0.25 (70%), C=$0.20 (65%)
- **Health thresholds**: <50% healthy, >=50% degraded, >=80% down, <5 samples default healthy
- **Error format**: `{code, messages}` matching Yuno error response pattern
- **Detailed reasoning**: Every classification and retry attempt includes 1-2 sentence explanation with processor health data, costs, and alternative-rejection logic
- **Chain-level narrative**: Final result includes full narrative showing each attempt outcome and cumulative cost
- **Adaptive learning** (Stretch A): Per-(failure_code, processor) success rates learned from outcomes
- **Cost optimization** (Stretch B): Multi-criteria ranking: adaptive rate DESC → cost ASC → failure rate ASC
- **Health dashboard** (Stretch C): Real-time processor states via API
- **Graceful shutdown**: Server handles SIGINT/SIGTERM with 10-second drain timeout
- **Generator auto-reset**: `POST /test/generate` resets store and health tracker for idempotent repeated calls

## Testing

50+ unit tests with testify, run with `-race` flag. All deterministic (seeded RNG).

- `internal/classifier/classifier_test.go`: 11 tests (all 9 codes + unknown + empty)
- `internal/health/tracker_test.go`: 10 tests (states, thresholds, window expiry, recovery)
- `internal/orchestrator/orchestrator_test.go`: 10 tests (hard/soft, max retries, alt processor, cost)
- `internal/metrics/calculator_test.go`: 4 tests (empty, mixed, aggregation, time filtering)
- `internal/api/handler_test.go`: 12 tests (HTTP handlers: health, failures, validation, 404, 409, batch)
- `internal/store/memory_test.go`: 11 tests (CRUD, duplicates, filters, pagination, reset, time range)

## Conventions

- Go standard project layout with `internal/` packages
- Interfaces defined close to consumers
- Request DTOs have `Validate() []string` methods
- Responses use `respondJSON` and `respondError` helpers
- Domain errors are sentinel errors checked with `errors.Is`
- All JSON responses have `Content-Type: application/json` via middleware
