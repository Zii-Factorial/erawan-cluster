package pgsql

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

const (
	defaultPostgreSQLCluster = "main"
	defaultPostgresSuperuser = "postgres"
	defaultReplicationUser   = "replicator"
	defaultPatroniAdminUser  = "admin"
)

type Runner struct {
	ansibleBin       string
	deployPlaybook   string
	ansibleVerbosity int
	streamLogs       bool
	maxOutputChars   int
}

func NewRunner(ansibleBin, deployPlaybook string) *Runner {
	if strings.TrimSpace(ansibleBin) == "" {
		ansibleBin = "ansible-playbook"
	}
	return &Runner{
		ansibleBin:     ansibleBin,
		deployPlaybook: deployPlaybook,
		maxOutputChars: 8000,
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
	return r.run(ctx, cfg)
}

func (r *Runner) run(ctx context.Context, cfg runConfig) (result StepResult) {
	result = StepResult{
		Name:      cfg.step.Name,
		Status:    JobStatusRunning,
		StartedAt: time.Now().UTC(),
		ExitCode:  -1,
	}
	defer func() { result.EndedAt = time.Now().UTC() }()

	if strings.TrimSpace(r.deployPlaybook) == "" {
		result.Status = JobStatusFailed
		result.Message = "playbook path is not configured"
		return
	}

	workspace, err := os.MkdirTemp("", "pgsql-cluster-job-")
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

	extraVars := map[string]any{
		"deployment_job_id":           cfg.jobID,
		"cluster_name":                cfg.spec.ClusterName,
		"primary_ip":                  cfg.spec.PrimaryIP,
		"standby_ips":                 cfg.spec.StandbyIPs,
		"postgres_superuser":          defaultPostgresSuperuser,
		"postgres_superuser_password": cfg.secret.PostgresPassword,
		"replication_user":            defaultReplicationUser,
		"replication_password":        cfg.secret.ReplicatorPassword,
		"patroni_admin_user":          defaultPatroniAdminUser,
		"patroni_admin_password":      cfg.secret.AdminPassword,
		"new_user":                    cfg.spec.NewUser,
		"new_user_password":           cfg.secret.NewUserPassword,
		"new_user_ssl_required":       cfg.spec.NewUserSSLRequired,
		"new_db":                      cfg.spec.NewDB,
		"postgres_port":               cfg.spec.PostgresPort,
		"postgresql_cluster_name":     defaultPostgreSQLCluster,
		"patroni_scope":               cfg.spec.ClusterName,
		"patroni_namespace":           "/db/",
		"patroni_rest_port":           8008,
		"patroni_config_path":         "/etc/patroni/patroni.yml",
		"patroni_pgpass_path":         "/etc/patroni/patroni.pgpass",
		"etcd_config_path":            "/etc/etcd/etcd.conf",
		"etcd_cluster_token":          cfg.spec.ClusterName + "-etcd-cluster-token",
		"etcd_client_port":            2379,
		"etcd_peer_port":              2380,
		"step_timeout_seconds":        cfg.spec.StepTimeoutSeconds,
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
		r.deployPlaybook,
		"--tags", cfg.step.Tag,
		"--extra-vars", "@" + varsPath,
	}
	if r.ansibleVerbosity > 0 {
		args = append(args, "-"+strings.Repeat("v", r.ansibleVerbosity))
	}

	cmd := exec.CommandContext(stepCtx, r.ansibleBin, args...)
	cmd.Env = append(os.Environ(), "ANSIBLE_HOST_KEY_CHECKING=False")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if r.streamLogs {
		cmd.Stdout = io.MultiWriter(&stdout, os.Stdout)
		cmd.Stderr = io.MultiWriter(&stderr, os.Stderr)
	} else {
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	}

	err = cmd.Run()
	result.Stdout = trimOutput(stdout.String(), r.maxOutputChars)
	result.Stderr = trimOutput(stderr.String(), r.maxOutputChars)

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
		b.WriteString("      ansible_ssh_common_args: " + strconv.Quote("-o IdentitiesOnly=yes") + "\n")
	}

	writeHost("primary", spec.PrimaryIP)
	for i, ip := range spec.StandbyIPs {
		writeHost(fmt.Sprintf("standby_%d", i+1), ip)
	}

	b.WriteString("  children:\n")
	b.WriteString("    pgsql_primary:\n")
	b.WriteString("      hosts:\n")
	b.WriteString("        primary: {}\n")
	b.WriteString("    pgsql_standby:\n")
	b.WriteString("      hosts:\n")
	for i := range spec.StandbyIPs {
		b.WriteString(fmt.Sprintf("        standby_%d: {}\n", i+1))
	}
	return b.String()
}

func trimOutput(in string, max int) string {
	in = strings.TrimSpace(in)
	if max <= 0 {
		return in
	}
	if len(in) <= max {
		return in
	}
	return in[:max] + "\n...truncated..."
}
