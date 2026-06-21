package security

import (
	"fmt"
	"strings"
)

/**
 * ValidateAuthConfig enforces fail-closed authentication: an empty API key
 * disables auth entirely, which is only acceptable in development (env "dev").
 * In any other environment it returns an error so the process refuses to start
 * an unauthenticated control plane.
 *
 * Params:
 *   env string - the env string
 *   apiKey string - the apiKey string
 *
 * Returns:
 *   error - error value; non-nil when the operation fails
 */
func ValidateAuthConfig(env, apiKey string) error {
	if strings.TrimSpace(apiKey) == "" && !strings.EqualFold(strings.TrimSpace(env), "dev") {
		return fmt.Errorf("API_KEY is required when ENV != dev: refusing to start an unauthenticated control plane")
	}
	return nil
}
