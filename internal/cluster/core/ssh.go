package core

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// keyscanTimeout bounds how long ssh-keyscan waits for a host to answer.
const keyscanTimeout = 10 * time.Second

// knownHostsLocks serializes writes to a given known_hosts file across
// concurrently running jobs within this process.
var (
	knownHostsLocksMu sync.Mutex
	knownHostsLocks   = map[string]*sync.Mutex{}
)

func knownHostsLock(path string) *sync.Mutex {
	knownHostsLocksMu.Lock()
	defer knownHostsLocksMu.Unlock()
	l, ok := knownHostsLocks[path]
	if !ok {
		l = &sync.Mutex{}
		knownHostsLocks[path] = l
	}
	return l
}

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

/**
 * AnsibleEnv returns the environment overrides that switch Ansible's global host
 * key checking on or off to match the policy.
 *
 * Receiver:
 *   p SSHPolicy - value receiver; the method operates on a copy of the SSHPolicy
 *
 * Returns:
 *   []string - the resulting []string
 */
func (p SSHPolicy) AnsibleEnv() []string {
	if p.VerifyHostKeys {
		return []string{"ANSIBLE_HOST_KEY_CHECKING=True"}
	}
	return []string{"ANSIBLE_HOST_KEY_CHECKING=False"}
}

/**
 * SSHCommonArgs returns the value for a host's ansible_ssh_common_args. When
 * verification is on it enforces StrictHostKeyChecking=yes (optionally pinning a
 * known_hosts file); when off it preserves the previous permissive behavior.
 *
 * Receiver:
 *   p SSHPolicy - value receiver; the method operates on a copy of the SSHPolicy
 *
 * Returns:
 *   string - the resulting string
 */
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

/**
 * EnsureKnownHosts pins the SSH host key of every host not already present in
 * KnownHostsFile, using trust-on-first-use: it scans the key with ssh-keyscan
 * and appends it before Ansible ever connects. This removes the need to
 * manually pre-seed known_hosts before provisioning brand-new nodes, while
 * StrictHostKeyChecking=yes still applies on every subsequent connection, so a
 * real key change later (MITM, host reimage) fails loudly instead of being
 * silently trusted.
 *
 * When reset is true, any existing pinned entry for the given hosts is removed
 * first, so they are treated as unseen and re-pinned with whatever key they
 * currently present. This is an explicit, per-request escape hatch for the
 * legitimate case of a rebuilt/reimaged node presenting a new (but expected)
 * key — callers should only set it when the operator has explicitly asked to
 * trust the node's current key, since it defeats the loud-failure protection
 * above for exactly the hosts listed.
 *
 * It is a no-op unless VerifyHostKeys and KnownHostsFile are both set: with
 * verification off there is nothing to pin, and without a pinned file Ansible
 * falls back to the ambient ~/.ssh/known_hosts, which this process does not
 * own and must not silently mutate.
 *
 * Receiver:
 *   p SSHPolicy - value receiver; the method operates on a copy of the SSHPolicy
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   hosts []string - the hosts ([]string)
 *   port int - the port value
 *   reset bool - when true, forget any pinned key for hosts before re-pinning
 *
 * Returns:
 *   error - error value; non-nil when a host's key could not be pinned
 */
func (p SSHPolicy) EnsureKnownHosts(ctx context.Context, hosts []string, port int, reset bool) error {
	if !p.VerifyHostKeys || strings.TrimSpace(p.KnownHostsFile) == "" || len(hosts) == 0 {
		return nil
	}

	mu := knownHostsLock(p.KnownHostsFile)
	mu.Lock()
	defer mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(p.KnownHostsFile), 0o700); err != nil {
		return fmt.Errorf("create known_hosts directory: %w", err)
	}
	f, err := os.OpenFile(p.KnownHostsFile, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open known_hosts: %w", err)
	}
	defer f.Close()

	existing, err := io.ReadAll(f)
	if err != nil {
		return fmt.Errorf("read known_hosts: %w", err)
	}

	if reset {
		filtered, changed := removeKnownHostsEntries(existing, hosts)
		if changed {
			if err := f.Truncate(0); err != nil {
				return fmt.Errorf("truncate known_hosts: %w", err)
			}
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return fmt.Errorf("seek known_hosts: %w", err)
			}
			if _, err := f.Write(filtered); err != nil {
				return fmt.Errorf("rewrite known_hosts: %w", err)
			}
			existing = filtered
		}
	}

	var missing []string
	for _, host := range hosts {
		host = strings.TrimSpace(host)
		if host == "" || knownHostsHasEntry(existing, host) {
			continue
		}
		missing = append(missing, host)
	}
	if len(missing) == 0 {
		return nil
	}

	scanned, err := scanHostKeys(ctx, missing, port)
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte(scanned)); err != nil {
		return fmt.Errorf("write known_hosts: %w", err)
	}
	return nil
}

// knownHostsHasEntry reports whether known_hosts content already has an entry
// for host. ssh-keyscan output (the only thing this function writes) is
// always plaintext, so entries it wrote are always matched here; pre-existing
// hashed entries just get harmlessly re-scanned and appended alongside.
func knownHostsHasEntry(content []byte, host string) bool {
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "|") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		for _, marker := range strings.Split(fields[0], ",") {
			if normalizeHostMarker(marker) == host {
				return true
			}
		}
	}
	return false
}

// removeKnownHostsEntries strips every plaintext-marker line matching hosts
// from content, reporting whether anything was removed. Like
// knownHostsHasEntry, it cannot recognize pre-existing hashed entries (ssh's
// HashKnownHosts) — those are left in place, matching the existing dedupe
// limitation.
func removeKnownHostsEntries(content []byte, hosts []string) ([]byte, bool) {
	hostSet := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		hostSet[strings.TrimSpace(h)] = true
	}

	changed := false
	var kept []string
	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, "|") {
			fields := strings.Fields(trimmed)
			if len(fields) > 0 {
				matched := false
				for _, marker := range strings.Split(fields[0], ",") {
					if hostSet[normalizeHostMarker(marker)] {
						matched = true
						break
					}
				}
				if matched {
					changed = true
					continue
				}
			}
		}
		kept = append(kept, line)
	}
	if !changed {
		return content, false
	}
	out := strings.Join(kept, "\n")
	if out != "" {
		out += "\n"
	}
	return []byte(out), true
}

// normalizeHostMarker strips the "[host]:port" bracketing ssh-keyscan/ssh use
// for non-default ports, so entries scanned at a non-standard port still
// dedupe against the bare host names this package matches on.
func normalizeHostMarker(marker string) string {
	if strings.HasPrefix(marker, "[") {
		if end := strings.Index(marker, "]"); end > 0 {
			return marker[1:end]
		}
	}
	return marker
}

// scanHostKeys runs ssh-keyscan for hosts and returns known_hosts-formatted
// lines. It fails fast if any host does not answer within keyscanTimeout,
// rather than letting Ansible hang on a host-key prompt for its full step
// timeout.
func scanHostKeys(ctx context.Context, hosts []string, port int) (string, error) {
	scanCtx, cancel := context.WithTimeout(ctx, keyscanTimeout)
	defer cancel()

	args := []string{"-T", "5"}
	if port > 0 && port != 22 {
		args = append(args, "-p", strconv.Itoa(port))
	}
	args = append(args, hosts...)

	cmd := exec.CommandContext(scanCtx, "ssh-keyscan", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	out := strings.TrimSpace(stdout.String())
	scannedHosts := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(line, "#") {
			continue
		}
		scannedHosts[normalizeHostMarker(fields[0])] = true
	}

	for _, host := range hosts {
		if !scannedHosts[host] {
			detail := strings.TrimSpace(stderr.String())
			if detail == "" && runErr != nil {
				detail = runErr.Error()
			}
			if detail == "" {
				detail = "no response within timeout"
			}
			return "", fmt.Errorf("ssh-keyscan found no host key for %s on port %d: %s", host, port, detail)
		}
	}

	return out + "\n", nil
}
