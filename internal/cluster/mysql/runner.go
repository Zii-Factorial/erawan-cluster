package mysql

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Runner struct {
	ansibleBin       string
	deployPlaybook   string
	rollbackPlaybook string
	ansibleVerbosity int
	streamLogs       bool
	maxOutputChars   int
}

func NewRunner(ansibleBin, deployPlaybook, rollbackPlaybook string) *Runner {
	if strings.TrimSpace(ansibleBin) == "" {
		ansibleBin = "ansible-playbook"
	}
	return &Runner{
		ansibleBin:       ansibleBin,
		deployPlaybook:   deployPlaybook,
		rollbackPlaybook: rollbackPlaybook,
		maxOutputChars:   8000,
	}
}

func (r *Runner) SetDebug(verbosity int, streamLogs bool, maxOutputChars int) {
	if verbosity < 0 {
		verbosity = 0
	}
	r.ansibleVerbosity = verbosity
	r.streamLogs = streamLogs
	if maxOutputChars > 0 {
		r.maxOutputChars = maxOutputChars
	}
}

type runConfig struct {
	jobID   string
	spec    StoredSpec
	secret  SecretInput
	step    step
	timeout time.Duration
}

func (r *Runner) RunDeployStep(ctx context.Context, cfg runConfig) StepResult {
	return r.run(ctx, cfg, r.deployPlaybook)
}

func (r *Runner) RunRollback(ctx context.Context, jobID string, spec StoredSpec, secret SecretInput, timeout time.Duration) StepResult {
	cfg := runConfig{
		jobID:  jobID,
		spec:   spec,
		secret: secret,
		step: step{
			Name: "rollback",
			Tag:  "rollback",
		},
		timeout: timeout,
	}
	return r.run(ctx, cfg, r.rollbackPlaybook)
}

func (r *Runner) run(ctx context.Context, cfg runConfig, playbook string) (result StepResult) {
	result = StepResult{
		Name:      cfg.step.Name,
		Status:    JobStatusRunning,
		StartedAt: time.Now().UTC(),
		ExitCode:  -1,
	}
	defer func() { result.EndedAt = time.Now().UTC() }()

	if strings.TrimSpace(playbook) == "" {
		result.Status = JobStatusFailed
		result.Message = "playbook path is not configured"
		return
	}

	workspace, err := os.MkdirTemp("", "mysql-cluster-job-")
	if err != nil {
		result.Status = JobStatusFailed
		result.Message = fmt.Sprintf("create temp dir: %v", err)
		return
	}
	defer os.RemoveAll(workspace)

	inventoryPath := filepath.Join(workspace, "inventory.yml")
	varsPath := filepath.Join(workspace, "vars.json")

	if err := os.WriteFile(inventoryPath, []byte(buildInventoryYAML(cfg.spec)), 0o600); err != nil {
		result.Status = JobStatusFailed
		result.Message = fmt.Sprintf("write inventory: %v", err)
		return
	}

	stepTimeout := cfg.spec.StepTimeoutSeconds
	if stepTimeout <= 0 {
		stepTimeout = 900
	}
	extraVars := map[string]any{
		"cluster_name":           cfg.spec.ClusterName,
		"cluster_admin_username": cfg.spec.AdminUsername,
		"cluster_admin_password": cfg.secret.AdminPassword,
		"primary_ip":             cfg.spec.PrimaryIP,
		"standby_ips":            cfg.spec.StandbyIPs,
		"new_user":               cfg.spec.NewUser,
		"new_user_password":      cfg.secret.NewUserPassword,
		"new_user_ssl_required":  cfg.spec.NewUserSSLRequired,
		"new_db":                 cfg.spec.NewDB,
		"mysql_port":             cfg.spec.MySQLPort,
		"bootstrap_router":       cfg.spec.BootstrapRouter,
		"router_service_name":    "mysqlrouter-" + cfg.spec.ClusterName,
		"step_timeout_seconds":   stepTimeout,
	}

	sanitized, err := json.Marshal(extraVars)
	if err != nil {
		result.Status = JobStatusFailed
		result.Message = fmt.Sprintf("marshal vars: %v", err)
		return
	}

	if err := os.WriteFile(varsPath, sanitized, 0o600); err != nil {
		result.Status = JobStatusFailed
		result.Message = fmt.Sprintf("write vars: %v", err)
		return
	}

	runTimeout := cfg.timeout
	if runTimeout <= 0 {
		runTimeout = 15 * time.Minute
	}
	stepCtx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()

	args := []string{
		"-i", inventoryPath,
		playbook,
		"--tags", cfg.step.Tag,
		"--extra-vars", "@" + varsPath,
	}
	if r.ansibleVerbosity > 0 {
		args = append(args, "-"+strings.Repeat("v", r.ansibleVerbosity))
	}

	cmd := exec.CommandContext(stepCtx, r.ansibleBin, args...)
	cmd.Env = append(os.Environ(), "ANSIBLE_HOST_KEY_CHECKING=False")

	var stdout cappedBuffer
	var stderr cappedBuffer
	stdout.limit = r.maxOutputChars
	stderr.limit = r.maxOutputChars
	if r.streamLogs {
		cmd.Stdout = io.MultiWriter(&stdout, os.Stdout)
		cmd.Stderr = io.MultiWriter(&stderr, os.Stderr)
	} else {
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	}

	err = cmd.Run()
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()

	if err == nil {
		result.Status = JobStatusCompleted
		result.ExitCode = 0
		return
	}

	result.Status = JobStatusFailed
	if exitErr, ok := err.(*exec.ExitError); ok {
		result.ExitCode = exitErr.ExitCode()
	} else {
		result.ExitCode = 1
	}
	if stepCtx.Err() == context.DeadlineExceeded {
		result.Message = "step execution timed out"
		return
	}
	result.Message = fmt.Sprintf("ansible step failed: %v", err)
	return
}

func buildInventoryYAML(spec StoredSpec) string {
	var b strings.Builder
	b.WriteString("all:\n")
	b.WriteString("  hosts:\n")
	writeHost := func(name, ip string) {
		b.WriteString("    " + name + ":\n")
		b.WriteString("      ansible_host: " + strconv.Quote(ip) + "\n")
		b.WriteString("      ansible_user: " + strconv.Quote(spec.SSHUser) + "\n")
		b.WriteString(fmt.Sprintf("      ansible_port: %d\n", spec.SSHPort))
		b.WriteString("      ansible_become: true\n")
		b.WriteString("      ansible_become_method: sudo\n")
		b.WriteString("      ansible_become_user: root\n")
		b.WriteString("      ansible_become_flags: " + strconv.Quote("-n") + "\n")
		b.WriteString("      ansible_ssh_private_key_file: " + strconv.Quote(spec.SSHPrivateKeyPath) + "\n")
		b.WriteString("      ansible_ssh_common_args: " + strconv.Quote("-o IdentitiesOnly=yes -o StrictHostKeyChecking=no") + "\n")
	}

	writeHost("primary", spec.PrimaryIP)
	for i, ip := range spec.StandbyIPs {
		writeHost(fmt.Sprintf("standby_%d", i+1), ip)
	}

	b.WriteString("  children:\n")
	b.WriteString("    mysql_primary:\n")
	b.WriteString("      hosts:\n")
	b.WriteString("        primary: {}\n")
	b.WriteString("    mysql_standby:\n")
	b.WriteString("      hosts:\n")
	for i := range spec.StandbyIPs {
		b.WriteString(fmt.Sprintf("        standby_%d: {}\n", i+1))
	}
	return b.String()
}

// cappedBuffer is a write-capped bytes.Buffer. Writes beyond limit are dropped
// and a truncation marker is appended to String(). limit=0 means unlimited.
type cappedBuffer struct {
	buf     bytes.Buffer
	limit   int
	dropped bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.limit > 0 {
		avail := b.limit - b.buf.Len()
		if avail <= 0 {
			b.dropped = true
			return len(p), nil
		}
		if len(p) > avail {
			_, _ = b.buf.Write(p[:avail])
			b.dropped = true
			return len(p), nil
		}
	}
	return b.buf.Write(p)
}

func (b *cappedBuffer) String() string {
	s := strings.TrimSpace(b.buf.String())
	if b.dropped {
		s += "\n...truncated..."
	}
	return s
}
