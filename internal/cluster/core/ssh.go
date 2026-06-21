package core

import "fmt"

// SSHPolicy controls how Ansible authenticates the SSH host keys of cluster
// nodes. The zero value is intentionally insecure-only when explicitly built
// that way; callers should construct the secure default with
// NewSSHPolicy(true, knownHosts).
//
// Verifying host keys defends against man-in-the-middle / SSH session hijacking
// between the control plane and the managed nodes. It is the default; operators
// may opt out (e.g. for greenfield bootstrap where host keys are not yet known)
// by setting VerifyHostKeys=false.
type SSHPolicy struct {
	VerifyHostKeys bool
	KnownHostsFile string
}

// AnsibleEnv returns the environment overrides that switch Ansible's global host
// key checking on or off to match the policy.
func (p SSHPolicy) AnsibleEnv() []string {
	if p.VerifyHostKeys {
		return []string{"ANSIBLE_HOST_KEY_CHECKING=True"}
	}
	return []string{"ANSIBLE_HOST_KEY_CHECKING=False"}
}

// SSHCommonArgs returns the value for a host's ansible_ssh_common_args. When
// verification is on it enforces StrictHostKeyChecking=yes (optionally pinning a
// known_hosts file); when off it preserves the previous permissive behavior.
func (p SSHPolicy) SSHCommonArgs() string {
	if !p.VerifyHostKeys {
		return "-o IdentitiesOnly=yes -o StrictHostKeyChecking=no"
	}
	args := "-o IdentitiesOnly=yes -o StrictHostKeyChecking=yes"
	if p.KnownHostsFile != "" {
		args += fmt.Sprintf(" -o UserKnownHostsFile=%s", p.KnownHostsFile)
	}
	return args
}
