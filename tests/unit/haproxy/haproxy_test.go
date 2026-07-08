// Package haproxy_test holds black-box unit tests for internal/haproxy,
// exercising only its exported API.
package haproxy_test

import (
	"strings"
	"testing"

	"erawan-cluster/internal/haproxy"
)

func TestNormalizeNodeIPsAllowsIPsAndHostnames(t *testing.T) {
	got, err := haproxy.NormalizeNodeIPs([]string{"10.10.95.211", "db-router-2", "db-router-3.local"})
	if err != nil {
		t.Fatalf("NormalizeNodeIPs returned error: %v", err)
	}
	want := []string{"10.10.95.211", "db-router-2", "db-router-3.local"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected normalized hosts: got %v want %v", got, want)
	}
}

func TestNormalizeNodeIPsDeduplicates(t *testing.T) {
	got, err := haproxy.NormalizeNodeIPs([]string{"10.0.0.1", "10.0.0.1", "10.0.0.2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected duplicates removed, got %v", got)
	}
}

func TestNormalizeNodeIPsRejectsInvalid(t *testing.T) {
	for _, in := range [][]string{nil, {"bad_host_name"}, {"has space"}, {""}} {
		if _, err := haproxy.NormalizeNodeIPs(in); err == nil {
			t.Fatalf("expected %v to be rejected", in)
		}
	}
}

func TestValidatePort(t *testing.T) {
	if err := haproxy.ValidatePort(8080, "port"); err != nil {
		t.Fatalf("expected 8080 to be valid: %v", err)
	}
	for _, bad := range []int{0, -1, 70000} {
		if err := haproxy.ValidatePort(bad, "port"); err == nil {
			t.Fatalf("expected port %d to be rejected", bad)
		}
	}
}

func TestBuildMySQLConfigUsesPrimaryCheckHTTPChk(t *testing.T) {
	cfg := haproxy.BuildMySQLConfig(25010, []string{"10.10.255.102", "10.10.146.139"}, 3306, 9200)
	for _, want := range []string{
		"listen node_25010",
		"balance first",
		"option clitcpka",
		"option srvtcpka",
		"option httpchk GET /",
		"http-check expect status 200",
		"option redispatch 1",
		"check port 9200",
		"server db1 10.10.255.102:3306 check",
		"server db2 10.10.146.139:3306 check backup",
	} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("expected MySQL config to contain %q\n%s", want, cfg)
		}
	}
	if strings.Contains(cfg, "option mysql-check") {
		t.Fatalf("MySQL config must not contain %q", "option mysql-check")
	}
}

func TestBuildMySQLConfigCustomPrimaryCheckPort(t *testing.T) {
	cfg := haproxy.BuildMySQLConfig(25010, []string{"10.0.0.1"}, 3306, 9201)
	if !strings.Contains(cfg, "check port 9201") {
		t.Fatalf("expected custom primary-check port in config\n%s", cfg)
	}
}

func TestBuildPGSQLConfigUsesPatroniHTTPChk(t *testing.T) {
	cfg := haproxy.BuildPGSQLConfig(25432, []string{"10.0.0.1", "10.0.0.2"}, 5432, 8008)
	for _, want := range []string{
		"listen node_25432",
		"option httpchk GET /leader",
		"http-check expect status 200",
		"server db1 10.0.0.1:5432 check",
		"server db2 10.0.0.2:5432 check backup",
	} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("expected PostgreSQL config to contain %q\n%s", want, cfg)
		}
	}
}

func TestBuildPGSQLConfigCustomPatroniPort(t *testing.T) {
	cfg := haproxy.BuildPGSQLConfig(25432, []string{"10.0.0.1"}, 5432, 8009)
	if !strings.Contains(cfg, "check port 8009") {
		t.Fatalf("expected custom patroni port in config\n%s", cfg)
	}
}

func TestNewServiceRequiresTenantsDir(t *testing.T) {
	if _, err := haproxy.NewService("", nil, 0); err == nil {
		t.Fatal("expected NewService to require a tenants directory")
	}
	if _, err := haproxy.NewService(t.TempDir(), []string{"true"}, 0); err != nil {
		t.Fatalf("expected NewService to succeed with a valid dir: %v", err)
	}
}
