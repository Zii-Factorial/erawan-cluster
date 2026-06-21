package env

import (
	"os"
	"strconv"
	"strings"
)

/**
 * lookupAny.
 *
 * Params:
 *   keys ...string - the keys (...string)
 *
 * Returns:
 *   string - the resulting string
 *   bool - boolean result
 */
func lookupAny(keys ...string) (string, bool) {
	for _, key := range keys {
		if val, ok := os.LookupEnv(key); ok {
			return val, true
		}
	}
	return "", false
}

/**
 * GetString.
 *
 * Params:
 *   key string - the key string
 *   fallback string - the fallback string
 *
 * Returns:
 *   string - the resulting string
 */
func GetString(key, fallback string) string {
	val, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	return val
}

/**
 * GetStringAny.
 *
 * Params:
 *   keys []string - the keys ([]string)
 *   fallback string - the fallback string
 *
 * Returns:
 *   string - the resulting string
 */
func GetStringAny(keys []string, fallback string) string {
	val, ok := lookupAny(keys...)
	if !ok {
		return fallback
	}
	return val
}

/**
 * GetInt.
 *
 * Params:
 *   key string - the key string
 *   fallback int - the fallback value
 *
 * Returns:
 *   int - the resulting integer
 */
func GetInt(key string, fallback int) int {
	val, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}

	parsed, err := strconv.Atoi(val)
	if err != nil {
		return fallback
	}
	return parsed
}

/**
 * GetIntAny.
 *
 * Params:
 *   keys []string - the keys ([]string)
 *   fallback int - the fallback value
 *
 * Returns:
 *   int - the resulting integer
 */
func GetIntAny(keys []string, fallback int) int {
	val, ok := lookupAny(keys...)
	if !ok {
		return fallback
	}

	parsed, err := strconv.Atoi(val)
	if err != nil {
		return fallback
	}
	return parsed
}

/**
 * GetBool.
 *
 * Params:
 *   key string - the key string
 *   fallback bool - the fallback flag
 *
 * Returns:
 *   bool - boolean result
 */
func GetBool(key string, fallback bool) bool {
	val, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}

	switch strings.ToLower(strings.TrimSpace(val)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

/**
 * GetBoolAny.
 *
 * Params:
 *   keys []string - the keys ([]string)
 *   fallback bool - the fallback flag
 *
 * Returns:
 *   bool - boolean result
 */
func GetBoolAny(keys []string, fallback bool) bool {
	val, ok := lookupAny(keys...)
	if !ok {
		return fallback
	}

	switch strings.ToLower(strings.TrimSpace(val)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
