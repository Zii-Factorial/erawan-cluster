package mysql

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"erawan-cluster/internal/cluster/core"
)

type Runner struct {
	ansibleBin           string
	deployPlaybook       string
	rollbackPlaybook     string
	addMemberPlaybook    string
	removeMemberPlaybook string
	ansibleVerbosity     int
	streamLogs           bool
	maxOutputChars       int
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

func (r *Runner) SetAddMemberPlaybook(path string)    { r.addMemberPlaybook = path }
func (r *Runner) SetRemoveMemberPlaybook(path string) { r.removeMemberPlaybook = path }

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

type memberRunConfig struct {
	jobID    string
	spec     StoredSpec
	secret   SecretInput
	memberIP string
	force    bool
	timeout  time.Duration
}

func (r *Runner) RunDeployStep(ctx context.Context, cfg runConfig) StepResult {
	return r.run(ctx, cfg, r.deployPlaybook)
}

func (r *Runner) RunRollback(ctx context.Context, jobID string, spec StoredSpec, secret SecretInput, timeout time.Duration) StepResult {
	cfg := runConfig{
		jobID:   jobID,
		spec:    spec,
		secret:  secret,
		step:    step{Name: "rollback", Tag: "rollback"},
		timeout: timeout,
	}
	return r.run(ctx, cfg, r.rollbackPlaybook)
}

func (r *Runner) RunAddMember(ctx context.Context, cfg memberRunConfig) StepResult {
	return r.runMember(ctx, cfg, r.addMemberPlaybook, "add_member")
}

func (r *Runner) RunRemoveMember(ctx context.Context, cfg memberRunConfig) StepResult {
	return r.runMember(ctx, cfg, r.removeMemberPlaybook, "remove_member")
}

// run executes one tagged step of a deploy/rollback playbook.
func (r *Runner) run(ctx context.Context, cfg runConfig, playbook string) StepResult {
	stepTimeout := cfg.spec.StepTimeoutSeconds
	if stepTimeout <= 0 {
		stepTimeout = 900
	}
	extraVars := map[string]any{
		"cluster_name":               cfg.spec.ClusterName,
		"cluster_admin_username":     cfg.spec.AdminUsername,
		"cluster_admin_password":     cfg.secret.AdminPassword,
		"primary_ip":                 cfg.spec.PrimaryIP,
		"standby_ips":                cfg.spec.StandbyIPs,
		"new_user":                   cfg.spec.NewUser,
		"new_user_password":          cfg.secret.NewUserPassword,
		"new_user_ssl_required":      cfg.spec.NewUserSSLRequired,
		"new_db":                     cfg.spec.NewDB,
		"mysql_port":                 cfg.spec.MySQLPort,
		"erawan_mysql_major_version": cfg.spec.MySQLVersion,
		"assume_prepared":            cfg.spec.AssumePrepared,
		"bootstrap_router":           cfg.spec.BootstrapRouter,
		"router_service_name":        "mysqlrouter-" + cfg.spec.ClusterName,
		"step_timeout_seconds":       stepTimeout,
	}
	return core.AnsibleRun(ctx, core.AnsibleSpec{
		Bin:             r.ansibleBin,
		Playbook:        playbook,
		Inventory:       buildInventoryYAML(cfg.spec),
		ExtraVars:       extraVars,
		Tags:            []string{cfg.step.Tag},
		Verbosity:       r.ansibleVerbosity,
		StreamLogs:      r.streamLogs,
		MaxOutputChars:  r.maxOutputChars,
		Timeout:         cfg.timeout,
		StepName:        cfg.step.Name,
		WorkspacePrefix: "mysql-cluster-job-",
		Env:             ansibleEnv(),
	})
}

// runMember executes the add/remove-member playbook for a single node.
func (r *Runner) runMember(ctx context.Context, cfg memberRunConfig, playbook, stepName string) StepResult {
	stepTimeout := cfg.spec.StepTimeoutSeconds
	if stepTimeout <= 0 {
		stepTimeout = 900
	}

	var inventory string
	// effectiveStandbys reflects the expected post-operation standby list so that
	// verify_cluster's EXPECTED_CLUSTER_NODES count is correct for both add and remove.
	effectiveStandbys := make([]string, len(cfg.spec.StandbyIPs))
	copy(effectiveStandbys, cfg.spec.StandbyIPs)
	if stepName == "add_member" {
		inventory = buildAddMemberInventoryYAML(cfg.spec, cfg.memberIP)
		effectiveStandbys = append(effectiveStandbys, cfg.memberIP)
	} else {
		inventory = buildInventoryYAML(cfg.spec)
		filtered := effectiveStandbys[:0]
		for _, ip := range effectiveStandbys {
			if ip != cfg.memberIP {
				filtered = append(filtered, ip)
			}
		}
		effectiveStandbys = filtered
	}

	extraVars := map[string]any{
		"cluster_name":               cfg.spec.ClusterName,
		"cluster_admin_username":     cfg.spec.AdminUsername,
		"cluster_admin_password":     cfg.secret.AdminPassword,
		"primary_ip":                 cfg.spec.PrimaryIP,
		"standby_ips":                effectiveStandbys,
		"new_member_ip":              cfg.memberIP,
		"remove_member_ip":           cfg.memberIP,
		"force_remove":               cfg.force,
		"mysql_port":                 cfg.spec.MySQLPort,
		"erawan_mysql_major_version": cfg.spec.MySQLVersion,
		"assume_prepared":            cfg.spec.AssumePrepared,
		"bootstrap_router":           cfg.spec.BootstrapRouter,
		"router_service_name":        "mysqlrouter-" + cfg.spec.ClusterName,
		"step_timeout_seconds":       stepTimeout,
		"expected_cluster_nodes":     len(effectiveStandbys) + 1,
	}
	return core.AnsibleRun(ctx, core.AnsibleSpec{
		Bin:             r.ansibleBin,
		Playbook:        playbook,
		Inventory:       inventory,
		ExtraVars:       extraVars,
		Verbosity:       r.ansibleVerbosity,
		StreamLogs:      r.streamLogs,
		MaxOutputChars:  r.maxOutputChars,
		Timeout:         cfg.timeout,
		StepName:        stepName,
		WorkspacePrefix: "mysql-member-job-",
		Env:             ansibleEnv(),
	})
}

// ansibleEnv returns the environment overrides applied to every ansible-playbook
// invocation for this engine.
func ansibleEnv() []string {
	return []string{"ANSIBLE_HOST_KEY_CHECKING=False"}
}

func buildAddMemberInventoryYAML(spec StoredSpec, newMemberIP string) string {
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
	writeHost("new_member", newMemberIP)

	b.WriteString("  children:\n")
	b.WriteString("    mysql_primary:\n")
	b.WriteString("      hosts:\n")
	b.WriteString("        primary: {}\n")
	b.WriteString("    mysql_standby:\n")
	b.WriteString("      hosts:\n")
	for i := range spec.StandbyIPs {
		b.WriteString(fmt.Sprintf("        standby_%d: {}\n", i+1))
	}
	b.WriteString("    mysql_new_member:\n")
	b.WriteString("      hosts:\n")
	b.WriteString("        new_member: {}\n")
	return b.String()
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
