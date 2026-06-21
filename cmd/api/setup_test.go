package main

import "testing"

func TestValidateSecurityConfigFailsClosedWithoutAPIKey(t *testing.T) {
	cfg := runtimeConfig{server: config{env: "production", apiKey: ""}}
	if err := validateSecurityConfig(cfg); err == nil {
		t.Fatal("expected start-up to be refused without API_KEY when ENV != dev")
	}
}

func TestValidateSecurityConfigAllowsDevWithoutAPIKey(t *testing.T) {
	cfg := runtimeConfig{server: config{env: "dev", apiKey: ""}}
	if err := validateSecurityConfig(cfg); err != nil {
		t.Fatalf("expected dev to allow empty API_KEY, got %v", err)
	}
}

func TestValidateSecurityConfigAllowsProductionWithAPIKey(t *testing.T) {
	cfg := runtimeConfig{server: config{env: "production", apiKey: "secret"}}
	if err := validateSecurityConfig(cfg); err != nil {
		t.Fatalf("expected production with API_KEY to be allowed, got %v", err)
	}
}
