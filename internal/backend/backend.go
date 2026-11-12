// Package backend contains the targets secrets are written to.
package backend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/mbu147/vaultwarden-docker-secrets/internal/config"
)

// Backend stores secret values.
type Backend interface {
	// Current returns the hash of the stored value, if any.
	Current(ctx context.Context, s config.Secret) (hash string, exists bool, err error)
	// Write stores value. Implementations must apply it so consumers can see it.
	Write(ctx context.Context, s config.Secret, value []byte) error
}

// Hash returns the hex SHA-256 of a secret value.
func Hash(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
