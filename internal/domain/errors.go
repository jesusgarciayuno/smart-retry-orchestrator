package domain

import "errors"

var (
	ErrNotFound     = errors.New("not found")
	ErrDuplicateID  = errors.New("duplicate transaction ID")
	ErrInvalidInput = errors.New("invalid input")
)
