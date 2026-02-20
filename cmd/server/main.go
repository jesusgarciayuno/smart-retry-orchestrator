package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jesuslgarciah/smart-retry-orchestrator/datagen"
	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/api"
	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/classifier"
	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/health"
	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/metrics"
	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/orchestrator"
	"github.com/jesuslgarciah/smart-retry-orchestrator/internal/store"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// Initialize dependencies
	memStore := store.NewMemoryStore()
	cls := classifier.NewClassifier()
	healthTracker := health.NewHealthTracker()
	orch := orchestrator.New(cls, healthTracker, memStore)
	metricsCalc := metrics.NewCalculator(memStore, healthTracker)

	// Generator function: auto-resets store and health before generating
	// to ensure idempotent behavior on repeated calls.
	generator := func() (int, error) {
		memStore.Reset()
		healthTracker.Reset()

		events := datagen.GenerateEvents()
		processed := 0
		for _, event := range events {
			_, err := orch.ProcessFailure(event)
			if err != nil {
				log.Printf("Warning: failed to process generated event %s: %v", event.TransactionID, err)
				continue
			}
			processed++
		}
		return processed, nil
	}

	// Set up router
	r := chi.NewRouter()
	handler := api.NewHandler(orch, memStore, healthTracker, metricsCalc, generator)

	// Resolve OpenAPI spec path: try working directory first (Docker), then source location
	openapiPath := filepath.Join("docs", "openapi.yaml")
	if _, err := os.Stat(openapiPath); os.IsNotExist(err) {
		_, currentFile, _, _ := runtime.Caller(0)
		projectRoot := filepath.Dir(filepath.Dir(filepath.Dir(currentFile)))
		openapiPath = filepath.Join(projectRoot, "docs", "openapi.yaml")
	}
	handler.SetOpenAPIPath(openapiPath)

	handler.RegisterRoutes(r)

	// Create server with graceful shutdown support
	addr := fmt.Sprintf(":%s", port)
	srv := &http.Server{
		Addr:    addr,
		Handler: r,
	}

	// Start server in a goroutine
	go func() {
		log.Printf("Smart Retry Orchestrator starting on %s", addr)
		log.Printf("Health check: http://localhost:%s/health", port)
		log.Printf("API base: http://localhost:%s/api/v1", port)
		log.Printf("API docs: http://localhost:%s/docs", port)
		log.Printf("OpenAPI spec: http://localhost:%s/docs/openapi.yaml", port)

		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	// Wait for interrupt signal for graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}
	log.Println("Server stopped gracefully")
}
