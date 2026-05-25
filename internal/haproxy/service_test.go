package haproxy

import (
	"strings"
	"testing"
)

func TestNormalizeNodeIPsAllowsIPsAndHostnames(t *testing.T) {
	got, err := NormalizeNodeIPs([]string{"10.10.95.211", "db-router-2", "db-router-3.local"})
	if err != nil {
		t.Fatalf("NormalizeNodeIPs returned error: %v", err)
	}
	want := []string{"10.10.95.211", "db-router-2", "db-router-3.local"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected normalized hosts: got %v want %v", got, want)
	}
}

func TestNormalizeNodeIPsRejectsInvalidHostname(t *testing.T) {
	if _, err := NormalizeNodeIPs([]string{"bad_host_name"}); err == nil {
		t.Fatal("expected invalid hostname to be rejected")
	}
}

func TestBuildConfigContentUsesFailoverFriendlyTCPSettings(t *testing.T) {
	cfg := buildConfigContent(25010, []string{"10.10.255.102", "10.10.146.139"}, 6446)

	required := []string{
		"listen node_25010",
		"# Always use first available healthy server = deterministic primary routing",
		"balance first",
		"# TCP keepalive",
		"option clitcpka",
		"option srvtcpka",
		"timeout connect  1s",
		"timeout check    500ms",
		"timeout queue    10s",
		"timeout client   10m",
		"timeout server   10m",
		"option redispatch",
		"retries 2",
		"default-server inter 1s fastinter 200ms downinter 500ms fall 2 rise 2 on-marked-down shutdown-sessions on-marked-up shutdown-backup-sessions",
		"# MySQL Router write port — port 6446 = R/W, always primary",
		"# Use first server as primary, others as backup",
		"server db1 10.10.255.102:6446 check",
		"server db2 10.10.146.139:6446 check backup",
	}

	for _, want := range required {
		if !strings.Contains(cfg, want) {
			t.Fatalf("expected config to contain %q\nfull config:\n%s", want, cfg)
		}
	}
}
