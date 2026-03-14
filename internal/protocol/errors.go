package protocol

import "errors"

var (
	ErrAuthFailed      = errors.New("authentication failed")
	ErrPortExhausted   = errors.New("no available port in range")
	ErrTunnelNotFound  = errors.New("tunnel not found")
	ErrInvalidMessage  = errors.New("invalid message format")
	ErrEmptyToken      = errors.New("auth token is empty")
	ErrPayloadTooLarge = errors.New("payload exceeds maximum size")
)
