package adapter

import "errors"

// Sentinel errors returned by Adapter operations.
var (
	// ErrSessionNotFound is returned when a requested session does not exist on disk or in the database.
	ErrSessionNotFound = errors.New("session not found")

	// ErrTurnInProgress is returned when an agent is currently generating tokens or holding an active lock.
	ErrTurnInProgress = errors.New("agent is currently generating response")

	// ErrNoOutputFound is returned when a session has no assistant response available to relay.
	ErrNoOutputFound = errors.New("no assistant output available to relay")
)
