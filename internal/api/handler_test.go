package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/classifier"
	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/health"
	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/metrics"
	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/orchestrator"
	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestServer creates a fully wired test server with real dependencies.
func setupTestServer() *httptest.Server {
	memStore := store.NewMemoryStore()
	cls := classifier.NewClassifier()
	ht := health.NewHealthTracker()
	orch := orchestrator.New(cls, ht, memStore)
	mc := metrics.NewCalculator(memStore, ht)
	gen := func() (int, error) { return 0, nil }

	r := chi.NewRouter()
	h := NewHandler(orch, memStore, ht, mc, gen)
	h.RegisterRoutes(r)
	return httptest.NewServer(r)
}

// makeFailureBody returns a JSON string for a failure request with the given parameters.
func makeFailureBody(txnID, failureCode, processor string, amount float64) string {
	return fmt.Sprintf(`{
		"transaction_id": %q,
		"amount": %.2f,
		"currency": "USD",
		"failure_code": %q,
		"processor": %q
	}`, txnID, amount, failureCode, processor)
}

// parseJSON unmarshals the response body into a map.
func parseJSON(t *testing.T, resp *http.Response) map[string]interface{} {
	t.Helper()
	var result map[string]interface{}
	err := json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(t, err, "failed to decode response JSON")
	return result
}

// parseJSONArray unmarshals the response body into a slice.
func parseJSONArray(t *testing.T, resp *http.Response) []interface{} {
	t.Helper()
	var result []interface{}
	err := json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(t, err, "failed to decode response JSON array")
	return result
}

func TestHealthCheck(t *testing.T) {
	ts := setupTestServer()
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/health")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body := parseJSON(t, resp)
	assert.Equal(t, "ok", body["status"])
	assert.Equal(t, "1.0.0", body["version"])
}

func TestSubmitFailure_SoftDecline(t *testing.T) {
	ts := setupTestServer()
	defer ts.Close()

	payload := makeFailureBody("soft-001", "PROCESSOR_TIMEOUT", "PROCESSOR_A", 150.00)
	resp, err := http.Post(ts.URL+"/api/v1/failures", "application/json", strings.NewReader(payload))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	body := parseJSON(t, resp)

	// Check classification is present and indicates soft decline
	classification, ok := body["classification"].(map[string]interface{})
	require.True(t, ok, "classification should be an object")
	assert.Equal(t, "SOFT_DECLINE", classification["decline_type"])

	// Should retry must be true for soft declines
	assert.Equal(t, true, body["should_retry"])

	// Retry attempts array should have at least one entry
	retryAttempts, ok := body["retry_attempts"].([]interface{})
	require.True(t, ok, "retry_attempts should be an array")
	assert.Greater(t, len(retryAttempts), 0, "retry_attempts should have at least one entry")
}

func TestSubmitFailure_HardDecline(t *testing.T) {
	ts := setupTestServer()
	defer ts.Close()

	payload := makeFailureBody("hard-001", "FRAUD_SUSPECTED", "PROCESSOR_A", 200.00)
	resp, err := http.Post(ts.URL+"/api/v1/failures", "application/json", strings.NewReader(payload))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	body := parseJSON(t, resp)

	// Check classification indicates hard decline
	classification, ok := body["classification"].(map[string]interface{})
	require.True(t, ok, "classification should be an object")
	assert.Equal(t, "HARD_DECLINE", classification["decline_type"])

	// Should retry must be false for hard declines
	assert.Equal(t, false, body["should_retry"])
}

func TestSubmitFailure_InvalidJSON(t *testing.T) {
	ts := setupTestServer()
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/v1/failures", "application/json", strings.NewReader("not json"))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	body := parseJSON(t, resp)
	assert.Equal(t, "INVALID_JSON", body["code"])
}

func TestSubmitFailure_MissingFields(t *testing.T) {
	ts := setupTestServer()
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/v1/failures", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	body := parseJSON(t, resp)
	assert.Equal(t, "VALIDATION_ERROR", body["code"])

	messages, ok := body["messages"].([]interface{})
	require.True(t, ok, "messages should be an array")
	assert.Greater(t, len(messages), 0, "messages should contain at least one validation error")
}

func TestSubmitFailure_DuplicateID(t *testing.T) {
	ts := setupTestServer()
	defer ts.Close()

	payload := makeFailureBody("dup-001", "PROCESSOR_TIMEOUT", "PROCESSOR_A", 100.00)

	// First submission should succeed
	resp1, err := http.Post(ts.URL+"/api/v1/failures", "application/json", strings.NewReader(payload))
	require.NoError(t, err)
	defer resp1.Body.Close()
	assert.Equal(t, http.StatusCreated, resp1.StatusCode)

	// Second submission with the same transaction_id should be a conflict
	resp2, err := http.Post(ts.URL+"/api/v1/failures", "application/json", strings.NewReader(payload))
	require.NoError(t, err)
	defer resp2.Body.Close()

	assert.Equal(t, http.StatusConflict, resp2.StatusCode)

	body := parseJSON(t, resp2)
	assert.Equal(t, "DUPLICATE_TRANSACTION", body["code"])
}

func TestGetTransaction_NotFound(t *testing.T) {
	ts := setupTestServer()
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/transactions/nonexistent")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	body := parseJSON(t, resp)
	assert.Equal(t, "NOT_FOUND", body["code"])
}

func TestGetTransaction_Found(t *testing.T) {
	ts := setupTestServer()
	defer ts.Close()

	txnID := "found-001"
	payload := makeFailureBody(txnID, "PROCESSOR_TIMEOUT", "PROCESSOR_A", 75.00)

	// Submit a failure first
	resp1, err := http.Post(ts.URL+"/api/v1/failures", "application/json", strings.NewReader(payload))
	require.NoError(t, err)
	resp1.Body.Close()
	require.Equal(t, http.StatusCreated, resp1.StatusCode)

	// Now fetch the transaction by ID
	resp2, err := http.Get(ts.URL + "/api/v1/transactions/" + txnID)
	require.NoError(t, err)
	defer resp2.Body.Close()

	assert.Equal(t, http.StatusOK, resp2.StatusCode)

	body := parseJSON(t, resp2)
	assert.Equal(t, txnID, body["id"])
}

func TestListTransactions(t *testing.T) {
	ts := setupTestServer()
	defer ts.Close()

	// Submit 3 different failures
	for i := 1; i <= 3; i++ {
		txnID := fmt.Sprintf("list-%03d", i)
		payload := makeFailureBody(txnID, "PROCESSOR_TIMEOUT", "PROCESSOR_A", float64(50*i))
		resp, err := http.Post(ts.URL+"/api/v1/failures", "application/json", strings.NewReader(payload))
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusCreated, resp.StatusCode)
	}

	// List all transactions
	resp, err := http.Get(ts.URL + "/api/v1/transactions")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	txns := parseJSONArray(t, resp)
	assert.Equal(t, 3, len(txns), "should return exactly 3 transactions")
}

func TestGetProcessorHealth(t *testing.T) {
	ts := setupTestServer()
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/processors/health")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body := parseJSON(t, resp)

	// All three processors should be present as top-level keys
	_, hasA := body["PROCESSOR_A"]
	_, hasB := body["PROCESSOR_B"]
	_, hasC := body["PROCESSOR_C"]
	assert.True(t, hasA, "response should contain PROCESSOR_A")
	assert.True(t, hasB, "response should contain PROCESSOR_B")
	assert.True(t, hasC, "response should contain PROCESSOR_C")
}

func TestRecoveryMetrics(t *testing.T) {
	ts := setupTestServer()
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/metrics/recovery")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body := parseJSON(t, resp)
	_, hasTotalTransactions := body["total_transactions"]
	assert.True(t, hasTotalTransactions, "response should contain total_transactions field")
}

func TestSubmitBatch(t *testing.T) {
	ts := setupTestServer()
	defer ts.Close()

	batchPayload := `{
		"events": [
			{
				"transaction_id": "batch-001",
				"amount": 100.00,
				"currency": "USD",
				"failure_code": "PROCESSOR_TIMEOUT",
				"processor": "PROCESSOR_A"
			},
			{
				"transaction_id": "batch-002",
				"amount": 200.00,
				"currency": "USD",
				"failure_code": "NETWORK_ERROR",
				"processor": "PROCESSOR_B"
			},
			{
				"transaction_id": "batch-003",
				"amount": 300.00,
				"currency": "EUR",
				"failure_code": "FRAUD_SUSPECTED",
				"processor": "PROCESSOR_C"
			}
		]
	}`

	resp, err := http.Post(ts.URL+"/api/v1/failures/batch", "application/json", bytes.NewBufferString(batchPayload))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	body := parseJSON(t, resp)
	total, ok := body["total"].(float64)
	require.True(t, ok, "total should be a number")
	assert.Equal(t, float64(3), total, "total should be 3")
}

// ---------- Table-driven tests for classification across multiple failure codes ----------

func TestSubmitFailure_ClassificationTableDriven(t *testing.T) {
	tests := []struct {
		name              string
		failureCode       string
		expectedDecline   string
		expectedRetry     bool
		expectRetryChain  bool // true if retry_attempts should have entries
	}{
		{
			name:             "PROCESSOR_TIMEOUT is soft decline with retries",
			failureCode:      "PROCESSOR_TIMEOUT",
			expectedDecline:  "SOFT_DECLINE",
			expectedRetry:    true,
			expectRetryChain: true,
		},
		{
			name:             "NETWORK_ERROR is soft decline with retries",
			failureCode:      "NETWORK_ERROR",
			expectedDecline:  "SOFT_DECLINE",
			expectedRetry:    true,
			expectRetryChain: true,
		},
		{
			name:             "ISSUER_UNAVAILABLE is soft decline with retries",
			failureCode:      "ISSUER_UNAVAILABLE",
			expectedDecline:  "SOFT_DECLINE",
			expectedRetry:    true,
			expectRetryChain: true,
		},
		{
			name:             "DO_NOT_HONOR is soft decline with retries",
			failureCode:      "DO_NOT_HONOR",
			expectedDecline:  "SOFT_DECLINE",
			expectedRetry:    true,
			expectRetryChain: true,
		},
		{
			name:             "RATE_LIMIT_EXCEEDED is soft decline with retries",
			failureCode:      "RATE_LIMIT_EXCEEDED",
			expectedDecline:  "SOFT_DECLINE",
			expectedRetry:    true,
			expectRetryChain: true,
		},
		{
			name:             "INSUFFICIENT_FUNDS is hard decline without retries",
			failureCode:      "INSUFFICIENT_FUNDS",
			expectedDecline:  "HARD_DECLINE",
			expectedRetry:    false,
			expectRetryChain: false,
		},
		{
			name:             "CARD_EXPIRED is hard decline without retries",
			failureCode:      "CARD_EXPIRED",
			expectedDecline:  "HARD_DECLINE",
			expectedRetry:    false,
			expectRetryChain: false,
		},
		{
			name:             "INVALID_CARD is hard decline without retries",
			failureCode:      "INVALID_CARD",
			expectedDecline:  "HARD_DECLINE",
			expectedRetry:    false,
			expectRetryChain: false,
		},
		{
			name:             "FRAUD_SUSPECTED is hard decline without retries",
			failureCode:      "FRAUD_SUSPECTED",
			expectedDecline:  "HARD_DECLINE",
			expectedRetry:    false,
			expectRetryChain: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Each sub-test gets its own server to avoid cross-contamination
			ts := setupTestServer()
			defer ts.Close()

			txnID := "tbl-" + tc.failureCode
			payload := makeFailureBody(txnID, tc.failureCode, "PROCESSOR_A", 100.00)

			resp, err := http.Post(ts.URL+"/api/v1/failures", "application/json", strings.NewReader(payload))
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusCreated, resp.StatusCode)

			body := parseJSON(t, resp)

			classification, ok := body["classification"].(map[string]interface{})
			require.True(t, ok, "classification should be an object")
			assert.Equal(t, tc.expectedDecline, classification["decline_type"])
			assert.Equal(t, tc.expectedRetry, body["should_retry"])

			if tc.expectRetryChain {
				retryAttempts, ok := body["retry_attempts"].([]interface{})
				require.True(t, ok, "retry_attempts should be an array for soft declines")
				assert.Greater(t, len(retryAttempts), 0, "retry_attempts should not be empty for soft declines")
			}
		})
	}
}

// ---------- Table-driven tests for error responses ----------

func TestSubmitFailure_ErrorResponses(t *testing.T) {
	tests := []struct {
		name           string
		body           string
		expectedStatus int
		expectedCode   string
	}{
		{
			name:           "invalid JSON body",
			body:           "not json",
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "INVALID_JSON",
		},
		{
			name:           "empty object missing all fields",
			body:           "{}",
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "VALIDATION_ERROR",
		},
		{
			name:           "missing transaction_id",
			body:           `{"amount":100,"currency":"USD","failure_code":"PROCESSOR_TIMEOUT","processor":"PROCESSOR_A"}`,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "VALIDATION_ERROR",
		},
		{
			name:           "negative amount",
			body:           `{"transaction_id":"err-neg","amount":-5,"currency":"USD","failure_code":"PROCESSOR_TIMEOUT","processor":"PROCESSOR_A"}`,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "VALIDATION_ERROR",
		},
		{
			name:           "invalid currency",
			body:           `{"transaction_id":"err-cur","amount":100,"currency":"XYZ","failure_code":"PROCESSOR_TIMEOUT","processor":"PROCESSOR_A"}`,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "VALIDATION_ERROR",
		},
		{
			name:           "invalid processor",
			body:           `{"transaction_id":"err-proc","amount":100,"currency":"USD","failure_code":"PROCESSOR_TIMEOUT","processor":"PROCESSOR_Z"}`,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "VALIDATION_ERROR",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts := setupTestServer()
			defer ts.Close()

			resp, err := http.Post(ts.URL+"/api/v1/failures", "application/json", strings.NewReader(tc.body))
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, tc.expectedStatus, resp.StatusCode)

			body := parseJSON(t, resp)
			assert.Equal(t, tc.expectedCode, body["code"])

			if tc.expectedCode == "VALIDATION_ERROR" {
				messages, ok := body["messages"].([]interface{})
				require.True(t, ok, "messages should be an array")
				assert.Greater(t, len(messages), 0, "messages should have at least one entry")
			}
		})
	}
}

// ---------- Table-driven tests for GET transaction scenarios ----------

func TestGetTransaction_Scenarios(t *testing.T) {
	tests := []struct {
		name           string
		txnID          string
		setupPayload   string // if non-empty, POST this first
		expectedStatus int
		checkBody      func(t *testing.T, body map[string]interface{})
	}{
		{
			name:           "returns 404 for nonexistent transaction",
			txnID:          "does-not-exist-123",
			expectedStatus: http.StatusNotFound,
			checkBody: func(t *testing.T, body map[string]interface{}) {
				assert.Equal(t, "NOT_FOUND", body["code"])
			},
		},
		{
			name:           "returns 200 with correct ID for existing transaction",
			txnID:          "scenario-found-001",
			setupPayload:   makeFailureBody("scenario-found-001", "CARD_EXPIRED", "PROCESSOR_B", 250.00),
			expectedStatus: http.StatusOK,
			checkBody: func(t *testing.T, body map[string]interface{}) {
				assert.Equal(t, "scenario-found-001", body["id"])
				classification, ok := body["classification"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, "HARD_DECLINE", classification["decline_type"])
			},
		},
		{
			name:           "soft decline transaction is retrievable",
			txnID:          "scenario-soft-001",
			setupPayload:   makeFailureBody("scenario-soft-001", "NETWORK_ERROR", "PROCESSOR_C", 500.00),
			expectedStatus: http.StatusOK,
			checkBody: func(t *testing.T, body map[string]interface{}) {
				assert.Equal(t, "scenario-soft-001", body["id"])
				classification, ok := body["classification"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, "SOFT_DECLINE", classification["decline_type"])
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts := setupTestServer()
			defer ts.Close()

			// Optionally submit a failure first to set up state
			if tc.setupPayload != "" {
				resp, err := http.Post(ts.URL+"/api/v1/failures", "application/json", strings.NewReader(tc.setupPayload))
				require.NoError(t, err)
				resp.Body.Close()
				require.Equal(t, http.StatusCreated, resp.StatusCode)
			}

			resp, err := http.Get(ts.URL + "/api/v1/transactions/" + tc.txnID)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, tc.expectedStatus, resp.StatusCode)

			body := parseJSON(t, resp)
			tc.checkBody(t, body)
		})
	}
}

// ---------- Batch validation tests ----------

func TestSubmitBatch_Validation(t *testing.T) {
	tests := []struct {
		name           string
		body           string
		expectedStatus int
		expectedCode   string
	}{
		{
			name:           "invalid JSON",
			body:           "not json at all",
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "INVALID_JSON",
		},
		{
			name:           "empty events array",
			body:           `{"events":[]}`,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "VALIDATION_ERROR",
		},
		{
			name:           "missing events field",
			body:           `{}`,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "VALIDATION_ERROR",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts := setupTestServer()
			defer ts.Close()

			resp, err := http.Post(ts.URL+"/api/v1/failures/batch", "application/json", strings.NewReader(tc.body))
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, tc.expectedStatus, resp.StatusCode)

			body := parseJSON(t, resp)
			assert.Equal(t, tc.expectedCode, body["code"])
		})
	}
}

// ---------- Content-Type header verification ----------

func TestResponseContentType(t *testing.T) {
	ts := setupTestServer()
	defer ts.Close()

	endpoints := []struct {
		method string
		path   string
	}{
		{"GET", "/health"},
		{"GET", "/api/v1/processors/health"},
		{"GET", "/api/v1/metrics/recovery"},
		{"GET", "/api/v1/transactions"},
	}

	for _, ep := range endpoints {
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			req, err := http.NewRequest(ep.method, ts.URL+ep.path, nil)
			require.NoError(t, err)

			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			contentType := resp.Header.Get("Content-Type")
			assert.Contains(t, contentType, "application/json", "Content-Type should be application/json")
		})
	}
}

// ---------- Request ID middleware verification ----------

func TestRequestIDMiddleware(t *testing.T) {
	ts := setupTestServer()
	defer ts.Close()

	// Without providing a request ID, the server should generate one
	resp, err := http.Get(ts.URL + "/health")
	require.NoError(t, err)
	defer resp.Body.Close()

	requestID := resp.Header.Get("X-Request-ID")
	assert.NotEmpty(t, requestID, "server should generate an X-Request-ID header")

	// With a provided request ID, the server should echo it back
	req, err := http.NewRequest("GET", ts.URL+"/health", nil)
	require.NoError(t, err)
	req.Header.Set("X-Request-ID", "custom-id-12345")

	resp2, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp2.Body.Close()

	assert.Equal(t, "custom-id-12345", resp2.Header.Get("X-Request-ID"))
}

// ---------- List transactions with filters ----------

func TestListTransactions_WithFilters(t *testing.T) {
	ts := setupTestServer()
	defer ts.Close()

	// Submit a mix of hard and soft declines across different processors
	submissions := []struct {
		txnID       string
		failureCode string
		processor   string
	}{
		{"filter-001", "PROCESSOR_TIMEOUT", "PROCESSOR_A"},
		{"filter-002", "FRAUD_SUSPECTED", "PROCESSOR_B"},
		{"filter-003", "NETWORK_ERROR", "PROCESSOR_A"},
		{"filter-004", "CARD_EXPIRED", "PROCESSOR_C"},
	}

	for _, s := range submissions {
		payload := makeFailureBody(s.txnID, s.failureCode, s.processor, 100.00)
		resp, err := http.Post(ts.URL+"/api/v1/failures", "application/json", strings.NewReader(payload))
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusCreated, resp.StatusCode)
	}

	t.Run("limit parameter restricts results", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/api/v1/transactions?limit=2")
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		txns := parseJSONArray(t, resp)
		assert.Equal(t, 2, len(txns))
	})

	t.Run("status filter works", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/api/v1/transactions?status=HARD_DECLINED")
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		txns := parseJSONArray(t, resp)
		// FRAUD_SUSPECTED and CARD_EXPIRED are both hard declines
		assert.Equal(t, 2, len(txns))
	})

	t.Run("processor filter works", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/api/v1/transactions?processor=PROCESSOR_A")
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		txns := parseJSONArray(t, resp)
		assert.Equal(t, 2, len(txns))
	})
}

// ---------- Recovery metrics after submitting data ----------

func TestRecoveryMetrics_AfterSubmissions(t *testing.T) {
	ts := setupTestServer()
	defer ts.Close()

	// Submit some failures so metrics have data
	payloads := []string{
		makeFailureBody("metrics-001", "PROCESSOR_TIMEOUT", "PROCESSOR_A", 100.00),
		makeFailureBody("metrics-002", "FRAUD_SUSPECTED", "PROCESSOR_B", 200.00),
		makeFailureBody("metrics-003", "NETWORK_ERROR", "PROCESSOR_C", 300.00),
	}

	for _, p := range payloads {
		resp, err := http.Post(ts.URL+"/api/v1/failures", "application/json", strings.NewReader(p))
		require.NoError(t, err)
		resp.Body.Close()
	}

	resp, err := http.Get(ts.URL + "/api/v1/metrics/recovery")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body := parseJSON(t, resp)

	totalTxns, ok := body["total_transactions"].(float64)
	require.True(t, ok, "total_transactions should be a number")
	assert.Equal(t, float64(3), totalTxns, "should have 3 total transactions")

	// Hard declines should be 1 (FRAUD_SUSPECTED)
	hardDeclines, ok := body["hard_declines"].(float64)
	require.True(t, ok)
	assert.Equal(t, float64(1), hardDeclines)

	// Soft declines should be 2 (PROCESSOR_TIMEOUT + NETWORK_ERROR)
	softDeclines, ok := body["soft_declines"].(float64)
	require.True(t, ok)
	assert.Equal(t, float64(2), softDeclines)

	// failure_code_breakdown should exist
	_, hasBreakdown := body["failure_code_breakdown"]
	assert.True(t, hasBreakdown, "response should have failure_code_breakdown")
}

// ---------- Batch with mixed valid and invalid events ----------

func TestSubmitBatch_MixedResults(t *testing.T) {
	ts := setupTestServer()
	defer ts.Close()

	batchPayload := `{
		"events": [
			{
				"transaction_id": "bmix-001",
				"amount": 100.00,
				"currency": "USD",
				"failure_code": "PROCESSOR_TIMEOUT",
				"processor": "PROCESSOR_A"
			},
			{
				"transaction_id": "",
				"amount": -5,
				"currency": "",
				"failure_code": "",
				"processor": ""
			},
			{
				"transaction_id": "bmix-003",
				"amount": 300.00,
				"currency": "EUR",
				"failure_code": "CARD_EXPIRED",
				"processor": "PROCESSOR_C"
			}
		]
	}`

	resp, err := http.Post(ts.URL+"/api/v1/failures/batch", "application/json", bytes.NewBufferString(batchPayload))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	body := parseJSON(t, resp)

	total, _ := body["total"].(float64)
	assert.Equal(t, float64(3), total)

	processed, _ := body["processed"].(float64)
	assert.Equal(t, float64(2), processed, "two valid events should be processed")

	errors, _ := body["errors"].(float64)
	assert.Equal(t, float64(1), errors, "one invalid event should produce an error")

	results, ok := body["results"].([]interface{})
	require.True(t, ok)
	assert.Equal(t, 3, len(results), "should have 3 results matching 3 inputs")
}

// ---------- Processor metrics endpoint ----------

func TestGetProcessorMetrics(t *testing.T) {
	ts := setupTestServer()
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/metrics/processors")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body := parseJSON(t, resp)

	// Should have entries for all three processors
	_, hasA := body["PROCESSOR_A"]
	_, hasB := body["PROCESSOR_B"]
	_, hasC := body["PROCESSOR_C"]
	assert.True(t, hasA, "should have PROCESSOR_A metrics")
	assert.True(t, hasB, "should have PROCESSOR_B metrics")
	assert.True(t, hasC, "should have PROCESSOR_C metrics")
}

// ---------- Adaptive strategy endpoint ----------

func TestGetAdaptiveStrategy(t *testing.T) {
	ts := setupTestServer()
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/strategy")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body := parseJSON(t, resp)
	_, hasWeights := body["adaptive_weights"]
	assert.True(t, hasWeights, "response should contain adaptive_weights")

	_, hasDescription := body["description"]
	assert.True(t, hasDescription, "response should contain description")
}

// ---------- Test data generation and reset ----------

func TestGenerateAndResetTestData(t *testing.T) {
	ts := setupTestServer()
	defer ts.Close()

	// Generate test data
	resp, err := http.Post(ts.URL+"/api/v1/test/generate", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	body := parseJSON(t, resp)
	assert.Equal(t, "generated", body["status"])

	// Reset all data
	resp2, err := http.Post(ts.URL+"/api/v1/test/reset", "application/json", nil)
	require.NoError(t, err)
	defer resp2.Body.Close()

	assert.Equal(t, http.StatusOK, resp2.StatusCode)

	resetBody := parseJSON(t, resp2)
	assert.Equal(t, "reset", resetBody["status"])

	// Verify transactions list is empty after reset
	resp3, err := http.Get(ts.URL + "/api/v1/transactions")
	require.NoError(t, err)
	defer resp3.Body.Close()

	assert.Equal(t, http.StatusOK, resp3.StatusCode)

	txns := parseJSONArray(t, resp3)
	assert.Equal(t, 0, len(txns), "transactions should be empty after reset")
}

// ---------- Retry history endpoint ----------

func TestGetRetryHistory(t *testing.T) {
	ts := setupTestServer()
	defer ts.Close()

	// Submit a soft decline to generate retry history
	txnID := "history-001"
	payload := makeFailureBody(txnID, "PROCESSOR_TIMEOUT", "PROCESSOR_A", 120.00)
	resp, err := http.Post(ts.URL+"/api/v1/failures", "application/json", strings.NewReader(payload))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	// Fetch retry history
	resp2, err := http.Get(ts.URL + "/api/v1/transactions/" + txnID + "/retries")
	require.NoError(t, err)
	defer resp2.Body.Close()

	assert.Equal(t, http.StatusOK, resp2.StatusCode)

	body := parseJSON(t, resp2)
	assert.Equal(t, txnID, body["transaction_id"])

	_, hasAttempts := body["attempts"]
	assert.True(t, hasAttempts, "response should contain attempts")

	_, hasDecisions := body["decisions"]
	assert.True(t, hasDecisions, "response should contain decisions")
}

func TestGetRetryHistory_NotFound(t *testing.T) {
	ts := setupTestServer()
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/transactions/nonexistent-txn/retries")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	body := parseJSON(t, resp)
	assert.Equal(t, "NOT_FOUND", body["code"])
}
