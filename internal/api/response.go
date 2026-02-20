package api

import (
	"encoding/json"
	"net/http"
)

// ErrorResponse follows the Yuno error response pattern.
type ErrorResponse struct {
	Code     string   `json:"code"`
	Messages []string `json:"messages"`
}

// BatchResponse summarizes batch processing results.
type BatchResponse struct {
	Total     int                    `json:"total"`
	Processed int                   `json:"processed"`
	Errors    int                    `json:"errors"`
	Results   []BatchItemResult      `json:"results"`
	Summary   BatchSummary           `json:"summary"`
}

// BatchItemResult is the result of processing one event in a batch.
type BatchItemResult struct {
	TransactionID string `json:"transaction_id"`
	Status        string `json:"status"`
	ShouldRetry   bool   `json:"should_retry"`
	Error         string `json:"error,omitempty"`
}

// BatchSummary aggregates batch statistics.
type BatchSummary struct {
	HardDeclines int `json:"hard_declines"`
	SoftDeclines int `json:"soft_declines"`
	Recovered    int `json:"recovered"`
	Exhausted    int `json:"exhausted"`
	Retrying     int `json:"retrying"`
}

// HealthResponse for the health check endpoint.
type HealthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

func respondJSON(w http.ResponseWriter, code int, data interface{}) {
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(data)
}

func respondError(w http.ResponseWriter, code int, errCode string, messages []string) {
	respondJSON(w, code, ErrorResponse{
		Code:     errCode,
		Messages: messages,
	})
}
