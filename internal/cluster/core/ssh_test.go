package core

import (
	"strings"
	"testing"
)

func TestSSHPolicySecureByDefault(t *testing.T) {
	p := SSHPolicy{VerifyHostKeys: true, KnownHostsFile: "/etc/erawan/known_hosts"}

	env := p.AnsibleEnv()
	if len(env) != 1 || env[0] != "ANSIBLE_HOST_KEY_CHECKING=True" {
		t.Fatalf("expected host key checking enabled, got %v", env)
	}
	args := p.SSHCommonArgs()
	if !strings.Contains(args, "StrictHostKeyChecking=yes") {
		t.Fatalf("expected StrictHostKeyChecking=yes, got %q", args)
	}
	if !strings.Contains(args, "UserKnownHostsFile=/etc/erawan/known_hosts") {
		t.Fatalf("expected known_hosts pin, got %q", args)
	}
}

func TestSSHPolicyInsecureOptOut(t *testing.T) {
	p := SSHPolicy{VerifyHostKeys: false}

	if env := p.AnsibleEnv(); env[0] != "ANSIBLE_HOST_KEY_CHECKING=False" {
		t.Fatalf("expected host key checking disabled, got %v", env)
	}
	if args := p.SSHCommonArgs(); !strings.Contains(args, "StrictHostKeyChecking=no") {
		t.Fatalf("expected StrictHostKeyChecking=no, got %q", args)
	}
}
