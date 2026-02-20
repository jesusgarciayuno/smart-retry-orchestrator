package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/domain"
	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/health"
	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/metrics"
	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/orchestrator"
	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/store"
)

// Handler holds dependencies and registers routes.
type Handler struct {
	orchestrator  orchestrator.RetryOrchestrator
	store         store.Store
	healthTracker health.HealthTracker
	metrics       metrics.MetricsCalculator
	generator     func() (int, error)
	openapiPath   string
}

// NewHandler creates a Handler with all dependencies.
func NewHandler(
	orch orchestrator.RetryOrchestrator,
	s store.Store,
	ht health.HealthTracker,
	mc metrics.MetricsCalculator,
	gen func() (int, error),
) *Handler {
	return &Handler{
		orchestrator:  orch,
		store:         s,
		healthTracker: ht,
		metrics:       mc,
		generator:     gen,
	}
}

// SetOpenAPIPath sets the filesystem path to the OpenAPI spec file.
func (h *Handler) SetOpenAPIPath(path string) {
	h.openapiPath = path
}

// RegisterRoutes mounts all API routes on the given Chi router.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Use(RequestID)
	r.Use(JSONContentType)
	r.Use(RequestLogger)

	r.Get("/health", h.healthCheck)
	r.Get("/docs/openapi.yaml", h.serveOpenAPI)
	r.Get("/docs", h.serveSwaggerUI)

	r.Route("/api/v1", func(r chi.Router) {
		// Transaction failure processing
		r.Post("/failures", h.submitFailure)
		r.Post("/failures/batch", h.submitBatch)

		// Retry outcome feedback (Stretch A)
		r.Post("/retries/{transactionID}/outcome", h.reportRetryOutcome)

		// Transaction history
		r.Get("/transactions", h.listTransactions)
		r.Get("/transactions/{transactionID}", h.getTransaction)
		r.Get("/transactions/{transactionID}/retries", h.getRetryHistory)

		// Processor health (Stretch C)
		r.Get("/processors/health", h.getProcessorHealth)

		// Metrics
		r.Get("/metrics/recovery", h.getRecoveryMetrics)
		r.Get("/metrics/processors", h.getProcessorMetrics)

		// Adaptive strategy (Stretch A)
		r.Get("/strategy", h.getAdaptiveStrategy)

		// Test data admin
		r.Post("/test/generate", h.generateTestData)
		r.Post("/test/reset", h.resetData)
	})
}

func (h *Handler) healthCheck(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, HealthResponse{
		Status:  "ok",
		Version: "1.0.0",
	})
}

func (h *Handler) serveSwaggerUI(w http.ResponseWriter, r *http.Request) {
	html := `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>Smart Retry Orchestrator - API Docs</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5.11.0/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5.11.0/swagger-ui-bundle.js"></script>
  <script>
    SwaggerUIBundle({
      url: "/docs/openapi.yaml",
      dom_id: '#swagger-ui',
      presets: [SwaggerUIBundle.presets.apis, SwaggerUIBundle.SwaggerUIStandalonePreset],
      layout: "BaseLayout"
    });
  </script>
</body>
</html>`
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(html))
}

func (h *Handler) serveOpenAPI(w http.ResponseWriter, r *http.Request) {
	if h.openapiPath == "" {
		respondError(w, http.StatusNotFound, "NOT_FOUND", []string{"OpenAPI spec path not configured"})
		return
	}
	data, err := os.ReadFile(h.openapiPath)
	if err != nil {
		respondError(w, http.StatusNotFound, "NOT_FOUND", []string{"OpenAPI spec file not found"})
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

func (h *Handler) submitFailure(w http.ResponseWriter, r *http.Request) {
	var req FailureRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "INVALID_JSON", []string{"invalid JSON body"})
		return
	}

	if errs := req.Validate(); len(errs) > 0 {
		respondError(w, http.StatusBadRequest, "VALIDATION_ERROR", errs)
		return
	}

	event := req.ToEvent()
	result, err := h.orchestrator.ProcessFailure(event)
	if err != nil {
		if errors.Is(err, domain.ErrDuplicateID) {
			respondError(w, http.StatusConflict, "DUPLICATE_TRANSACTION", []string{"transaction ID already exists"})
			return
		}
		respondError(w, http.StatusInternalServerError, "PROCESSING_ERROR", []string{err.Error()})
		return
	}

	respondJSON(w, http.StatusCreated, result)
}

func (h *Handler) submitBatch(w http.ResponseWriter, r *http.Request) {
	var req BatchFailureRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "INVALID_JSON", []string{"invalid JSON body"})
		return
	}

	if errs := req.Validate(); len(errs) > 0 {
		respondError(w, http.StatusBadRequest, "VALIDATION_ERROR", errs)
		return
	}

	resp := BatchResponse{
		Total:   len(req.Events),
		Results: make([]BatchItemResult, 0, len(req.Events)),
	}

	for _, eventReq := range req.Events {
		if errs := eventReq.Validate(); len(errs) > 0 {
			resp.Errors++
			resp.Results = append(resp.Results, BatchItemResult{
				TransactionID: eventReq.TransactionID,
				Status:        "error",
				Error:         errs[0],
			})
			continue
		}

		event := eventReq.ToEvent()
		result, err := h.orchestrator.ProcessFailure(event)
		if err != nil {
			resp.Errors++
			resp.Results = append(resp.Results, BatchItemResult{
				TransactionID: eventReq.TransactionID,
				Status:        "error",
				Error:         err.Error(),
			})
			continue
		}

		resp.Processed++
		resp.Results = append(resp.Results, BatchItemResult{
			TransactionID: result.TransactionID,
			Status:        string(result.FinalStatus),
			ShouldRetry:   result.ShouldRetry,
		})

		// Update summary
		switch result.Classification.DeclineType {
		case domain.HardDecline:
			resp.Summary.HardDeclines++
		case domain.SoftDecline:
			resp.Summary.SoftDeclines++
		}
		switch result.FinalStatus {
		case domain.StatusRecovered:
			resp.Summary.Recovered++
		case domain.StatusExhausted:
			resp.Summary.Exhausted++
		case domain.StatusRetrying:
			resp.Summary.Retrying++
		}
	}

	respondJSON(w, http.StatusCreated, resp)
}

func (h *Handler) reportRetryOutcome(w http.ResponseWriter, r *http.Request) {
	txnID := chi.URLParam(r, "transactionID")

	var req RetryOutcomeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "INVALID_JSON", []string{"invalid JSON body"})
		return
	}

	if errs := req.Validate(); len(errs) > 0 {
		respondError(w, http.StatusBadRequest, "VALIDATION_ERROR", errs)
		return
	}

	err := h.orchestrator.RecordRetryOutcome(txnID, domain.ProcessorName(req.Processor), req.Success)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			respondError(w, http.StatusNotFound, "NOT_FOUND", []string{"transaction not found"})
			return
		}
		respondError(w, http.StatusInternalServerError, "PROCESSING_ERROR", []string{err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "recorded"})
}

func (h *Handler) listTransactions(w http.ResponseWriter, r *http.Request) {
	filter := domain.TransactionFilter{}

	if s := r.URL.Query().Get("status"); s != "" {
		status := domain.TransactionStatus(s)
		filter.Status = &status
	}
	if p := r.URL.Query().Get("processor"); p != "" {
		proc := domain.ProcessorName(p)
		filter.Processor = &proc
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if limit, err := strconv.Atoi(l); err == nil && limit > 0 {
			filter.Limit = limit
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if offset, err := strconv.Atoi(o); err == nil && offset >= 0 {
			filter.Offset = offset
		}
	}

	if filter.Limit == 0 {
		filter.Limit = 50
	}

	txns, err := h.store.ListTransactions(filter)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "STORE_ERROR", []string{err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, txns)
}

func (h *Handler) getTransaction(w http.ResponseWriter, r *http.Request) {
	txnID := chi.URLParam(r, "transactionID")

	tx, err := h.store.GetTransaction(txnID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			respondError(w, http.StatusNotFound, "NOT_FOUND", []string{"transaction not found"})
			return
		}
		respondError(w, http.StatusInternalServerError, "STORE_ERROR", []string{err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, tx)
}

func (h *Handler) getRetryHistory(w http.ResponseWriter, r *http.Request) {
	txnID := chi.URLParam(r, "transactionID")

	tx, err := h.store.GetTransaction(txnID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			respondError(w, http.StatusNotFound, "NOT_FOUND", []string{"transaction not found"})
			return
		}
		respondError(w, http.StatusInternalServerError, "STORE_ERROR", []string{err.Error()})
		return
	}

	decisions, _ := h.store.GetDecisionLogs(txnID)

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"transaction_id": txnID,
		"retry_count":    tx.RetryCount,
		"attempts":       tx.RetryAttempts,
		"decisions":      decisions,
	})
}

func (h *Handler) getProcessorHealth(w http.ResponseWriter, r *http.Request) {
	allHealth := h.healthTracker.GetAllHealth()
	respondJSON(w, http.StatusOK, allHealth)
}

func (h *Handler) getRecoveryMetrics(w http.ResponseWriter, r *http.Request) {
	start, end, err := parseTimeRange(r)
	if err != nil {
		respondError(w, http.StatusBadRequest, "INVALID_PARAMS", []string{err.Error()})
		return
	}

	m, err := h.metrics.GetRecoveryMetrics(start, end)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "METRICS_ERROR", []string{err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, m)
}

func (h *Handler) getProcessorMetrics(w http.ResponseWriter, r *http.Request) {
	start, end, err := parseTimeRange(r)
	if err != nil {
		respondError(w, http.StatusBadRequest, "INVALID_PARAMS", []string{err.Error()})
		return
	}

	m, err := h.metrics.GetProcessorMetrics(start, end)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "METRICS_ERROR", []string{err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, m)
}

func (h *Handler) getAdaptiveStrategy(w http.ResponseWriter, r *http.Request) {
	weights := h.orchestrator.GetAdaptiveWeights()
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"adaptive_weights": weights,
		"description":      "Per-error-code, per-processor success rates learned from retry outcomes",
	})
}

func (h *Handler) generateTestData(w http.ResponseWriter, r *http.Request) {
	count, err := h.generator()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "GENERATOR_ERROR", []string{err.Error()})
		return
	}

	respondJSON(w, http.StatusCreated, map[string]interface{}{
		"status":    "generated",
		"count":     count,
		"message":   "Test events generated and processed successfully",
	})
}

func (h *Handler) resetData(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Reset(); err != nil {
		respondError(w, http.StatusInternalServerError, "RESET_ERROR", []string{err.Error()})
		return
	}

	h.healthTracker.Reset()

	respondJSON(w, http.StatusOK, map[string]string{
		"status":  "reset",
		"message": "All data cleared successfully",
	})
}

func parseTimeRange(r *http.Request) (time.Time, time.Time, error) {
	startStr := r.URL.Query().Get("start")
	endStr := r.URL.Query().Get("end")

	// Defaults: last 24 hours
	end := time.Now()
	start := end.Add(-24 * time.Hour)

	if startStr != "" {
		parsed, err := time.Parse(time.RFC3339, startStr)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("invalid start time format, use RFC3339")
		}
		start = parsed
	}

	if endStr != "" {
		parsed, err := time.Parse(time.RFC3339, endStr)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("invalid end time format, use RFC3339")
		}
		end = parsed
	}

	return start, end, nil
}
