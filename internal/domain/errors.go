package domain

import "errors"

var (
	ErrNotFound         = errors.New("not found")
	ErrMaxRetries       = errors.New("maximum retries exceeded")
	ErrDuplicateID      = errors.New("duplicate transaction ID")
	ErrInvalidInput     = errors.New("invalid input")
	ErrProcessorDown    = errors.New("all processors are down")
	ErrHardDecline      = errors.New("hard decline cannot be retried")
)
