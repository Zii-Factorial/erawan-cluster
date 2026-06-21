// Package env_test holds black-box unit tests for internal/env.
package env_test

import (
	"testing"

	"erawan-cluster/internal/env"
)

func TestGetString(t *testing.T) {
	if got := env.GetString("ERAWAN_TEST_UNSET", "fallback"); got != "fallback" {
		t.Fatalf("expected fallback, got %q", got)
	}
	t.Setenv("ERAWAN_TEST_STR", "value")
	if got := env.GetString("ERAWAN_TEST_STR", "fallback"); got != "value" {
		t.Fatalf("expected env value, got %q", got)
	}
}

func TestGetInt(t *testing.T) {
	if got := env.GetInt("ERAWAN_TEST_UNSET_INT", 7); got != 7 {
		t.Fatalf("expected fallback 7, got %d", got)
	}
	t.Setenv("ERAWAN_TEST_INT", "42")
	if got := env.GetInt("ERAWAN_TEST_INT", 7); got != 42 {
		t.Fatalf("expected 42, got %d", got)
	}
	t.Setenv("ERAWAN_TEST_BAD_INT", "not-a-number")
	if got := env.GetInt("ERAWAN_TEST_BAD_INT", 7); got != 7 {
		t.Fatalf("expected fallback for unparseable int, got %d", got)
	}
}

func TestGetBool(t *testing.T) {
	for _, truthy := range []string{"1", "true", "yes", "on", "TRUE"} {
		t.Setenv("ERAWAN_TEST_BOOL", truthy)
		if !env.GetBool("ERAWAN_TEST_BOOL", false) {
			t.Fatalf("expected %q to be true", truthy)
		}
	}
	for _, falsy := range []string{"0", "false", "no", "off"} {
		t.Setenv("ERAWAN_TEST_BOOL", falsy)
		if env.GetBool("ERAWAN_TEST_BOOL", true) {
			t.Fatalf("expected %q to be false", falsy)
		}
	}
	if !env.GetBool("ERAWAN_TEST_UNSET_BOOL", true) {
		t.Fatal("expected fallback true for unset bool")
	}
}

func TestGetIntAnyAndBoolAny(t *testing.T) {
	t.Setenv("ERAWAN_SECOND", "9")
	if got := env.GetIntAny([]string{"ERAWAN_FIRST_UNSET", "ERAWAN_SECOND"}, 0); got != 9 {
		t.Fatalf("expected first-set lookup to find 9, got %d", got)
	}
	t.Setenv("ERAWAN_FLAG", "on")
	if !env.GetBoolAny([]string{"ERAWAN_FLAG_UNSET", "ERAWAN_FLAG"}, false) {
		t.Fatal("expected GetBoolAny to find the set flag")
	}
}
