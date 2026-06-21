package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"erawan-cluster/internal/env"
)

// config holds the small set of process-wide settings that HTTP handlers need
// at request time: the listen address, environment name, API key for auth, the
// build version, and the HAProxy host used when dialing cluster metrics.
//
// It is deliberately kept narrow. Anything only needed once during start-up
// (playbook paths, state directories, Ansible tunables) lives in the *Config
// structs below and is consumed by the builders in setup.go, never carried on
// the long-lived application value.
type config struct {
	addr      string
	env       string
	apiKey    string
	version   string
	proxyHost string // HAProxy host for metric connections; from PROXY_HOST env (default 127.0.0.1)
}

// runtimeConfig is the fully-resolved configuration for the whole API process.
// It is loaded exactly once, at start-up, by loadConfig and then handed to the
// builders in setup.go. Grouping every tunable here (rather than reading the
// environment ad hoc throughout main) keeps configuration in one auditable
// place and makes adding a new engine a matter of adding one more field.
type runtimeConfig struct {
	server            config
	haproxy           haproxyConfig
	ssh               sshConfig
	mysql             clusterEngineConfig
	pgsql             clusterEngineConfig
	encryptionKey     string
	baseDir           string
	maxConcurrentJobs int  // cap on concurrent background cluster jobs (ansible runs)
	enablePprof       bool // expose /debug/pprof on the loopback interface
}

// haproxyConfig describes how the HAProxy service writes tenant fragments and
// reloads the proxy. tenantsDir is where per-tenant config files are written,
// reloadCmd is the argv used to reload HAProxy, reloadTimeout bounds that
// command, and mainConfigs lists the operator-managed base config files that
// must never be deleted by tenant operations.
type haproxyConfig struct {
	tenantsDir    string
	reloadCmd     []string
	reloadTimeout time.Duration
	mainConfigs   []string
}

// sshConfig holds the SSH credentials Ansible uses to reach cluster nodes. The
// same credentials are shared by every engine. When both fields are empty the
// runner falls back to the ambient SSH configuration of the host.
type sshConfig struct {
	user           string
	privateKeyPath string
}

// hasCredentials reports whether any explicit SSH credential was provided and
// therefore should be pushed into the cluster services. When false, the
// services keep their default (ambient) SSH behaviour.
func (s sshConfig) hasCredentials() bool {
	return strings.TrimSpace(s.user) != "" || strings.TrimSpace(s.privateKeyPath) != ""
}

// ansibleConfig captures the Ansible execution tunables for a single engine:
// which playbook binary to invoke and how verbose / how much captured output
// to retain when debugging a deploy.
type ansibleConfig struct {
	bin                string
	debug              bool
	verbosity          int
	stepOutputMaxChars int
}

// clusterEngineConfig is the per-engine configuration for a SQL cluster engine
// (currently MySQL and PostgreSQL). Every engine is provisioned the same way —
// a job state directory plus a set of Ansible playbooks — so a new engine only
// needs to populate one of these and call the matching builder in setup.go.
//
// rollbackPlaybook is engine-specific: MySQL supports rollback, PostgreSQL does
// not, so it is left empty for engines without that capability.
type clusterEngineConfig struct {
	stateDir             string
	deployPlaybook       string
	addMemberPlaybook    string
	removeMemberPlaybook string
	rollbackPlaybook     string
	ansible              ansibleConfig
}

// loadConfig reads the entire process configuration from the environment,
// applying sane defaults so the service can run unconfigured in development.
// It performs no I/O beyond resolving the working directory and never fails;
// validation of the resolved values happens in the builders.
func loadConfig() runtimeConfig {
	baseDir := projectBaseDir()
	ansibleBin := env.GetString("ANSIBLE_PLAYBOOK_BIN", "ansible-playbook")

	// All engines store their job history under a shared root by default, each
	// in its own sub-directory; an engine may override with its own *_STATE_DIR.
	sharedStateDir := env.GetString("CLUSTER_STATE_DIR", "/var/lib/erawan-cluster/cluster/jobs")

	mysqlCfg := loadClusterEngineConfig("mysql", baseDir, sharedStateDir, ansibleBin)
	mysqlCfg.rollbackPlaybook = env.GetString(
		"MYSQL_ROLLBACK_PLAYBOOK",
		filepath.Join(baseDir, "cluster/mysql/playbooks/rollback.yml"),
	)

	pgsqlCfg := loadClusterEngineConfig("pgsql", baseDir, sharedStateDir, ansibleBin)

	return runtimeConfig{
		server:            loadServerConfig(),
		haproxy:           loadHAProxyConfig(),
		ssh:               loadSSHConfig(),
		mysql:             mysqlCfg,
		pgsql:             pgsqlCfg,
		encryptionKey:     env.GetString("ENCRYPTION_KEY", ""),
		baseDir:           baseDir,
		maxConcurrentJobs: env.GetInt("CLUSTER_MAX_CONCURRENT_JOBS", 4),
		enablePprof:       env.GetBool("ENABLE_PPROF", false),
	}
}

// loadServerConfig resolves the HTTP server and request-time settings. The
// listen address may be given directly via API_ADDR, or assembled from
// API_HOST and API_PORT when API_ADDR is unset/blank.
func loadServerConfig() config {
	addr := env.GetString("API_ADDR", "")
	if strings.TrimSpace(addr) == "" {
		host := env.GetString("API_HOST", "0.0.0.0")
		port := env.GetString("API_PORT", "8080")
		addr = host + ":" + port
	}

	return config{
		addr:      addr,
		env:       env.GetString("ENV", "dev"),
		apiKey:    env.GetString("API_KEY", ""),
		version:   appVersion,
		proxyHost: env.GetString("PROXY_HOST", "127.0.0.1"),
	}
}

// loadHAProxyConfig resolves where tenant fragments live and how the proxy is
// reloaded. HAPROXY_MAIN_CONFIGS is an optional comma-separated list of base
// config files that tenant operations must leave untouched.
func loadHAProxyConfig() haproxyConfig {
	var mainConfigs []string
	if raw := env.GetString("HAPROXY_MAIN_CONFIGS", ""); raw != "" {
		mainConfigs = strings.Split(raw, ",")
	}

	return haproxyConfig{
		tenantsDir:    env.GetString("TENANTS_DIR", "/var/lib/erawan-cluster/haproxy/tenants"),
		reloadCmd:     parseCommand(env.GetString("HAPROXY_RELOAD_CMD", "sudo /bin/systemctl reload haproxy")),
		reloadTimeout: time.Duration(env.GetInt("HAPROXY_RELOAD_TIMEOUT_SECONDS", 15)) * time.Second,
		mainConfigs:   mainConfigs,
	}
}

// loadSSHConfig resolves the shared SSH credentials used by every cluster
// engine's Ansible runner.
func loadSSHConfig() sshConfig {
	return sshConfig{
		user:           env.GetString("CLUSTER_SSH_USER", ""),
		privateKeyPath: env.GetString("CLUSTER_SSH_PRIVATE_KEY_PATH", ""),
	}
}

// loadClusterEngineConfig resolves the playbook paths and state directory for
// one cluster engine, identified by its lowercase name (e.g. "mysql"). The name
// drives both the default on-disk layout (cluster/<name>/playbooks/...) and the
// uppercase environment-variable prefix (<NAME>_DEPLOY_PLAYBOOK, ...), so wiring
// a new engine is purely declarative.
func loadClusterEngineConfig(name, baseDir, sharedStateDir, ansibleBin string) clusterEngineConfig {
	prefix := strings.ToUpper(name)
	playbookDir := filepath.Join(baseDir, "cluster", name, "playbooks")

	return clusterEngineConfig{
		stateDir:             env.GetString(prefix+"_CLUSTER_STATE_DIR", filepath.Join(sharedStateDir, name)),
		deployPlaybook:       env.GetString(prefix+"_DEPLOY_PLAYBOOK", filepath.Join(playbookDir, "deploy.yml")),
		addMemberPlaybook:    env.GetString(prefix+"_ADD_MEMBER_PLAYBOOK", filepath.Join(playbookDir, "add_member.yml")),
		removeMemberPlaybook: env.GetString(prefix+"_REMOVE_MEMBER_PLAYBOOK", filepath.Join(playbookDir, "remove_member.yml")),
		ansible:              loadAnsibleConfig(prefix, ansibleBin),
	}
}

// loadAnsibleConfig resolves the Ansible execution tunables for one engine.
// Each value may be set globally (CLUSTER_*) or per engine (<PREFIX>_*); because
// the global key is checked first, a global setting takes precedence over the
// engine-specific one when both are present.
//
// As a convenience, turning debugging on without choosing explicit values bumps
// verbosity and the captured-output cap to diagnostic-friendly defaults.
func loadAnsibleConfig(prefix, bin string) ansibleConfig {
	debug := env.GetBoolAny([]string{"CLUSTER_ANSIBLE_DEBUG", prefix + "_ANSIBLE_DEBUG"}, false)
	verbosity := env.GetIntAny([]string{"CLUSTER_ANSIBLE_VERBOSITY", prefix + "_ANSIBLE_VERBOSITY"}, 0)
	stepOutputMaxChars := env.GetIntAny([]string{"CLUSTER_STEP_OUTPUT_MAX_CHARS", prefix + "_STEP_OUTPUT_MAX_CHARS"}, 8000)

	if debug && verbosity <= 0 {
		verbosity = 3
	}
	if debug && stepOutputMaxChars == 8000 {
		stepOutputMaxChars = 200000
	}

	return ansibleConfig{
		bin:                bin,
		debug:              debug,
		verbosity:          verbosity,
		stepOutputMaxChars: stepOutputMaxChars,
	}
}

// parseCommand splits a shell-style command string into an argv slice. If the
// input is empty it falls back to the default HAProxy reload command so the
// service always has something runnable.
func parseCommand(raw string) []string {
	parts := strings.Fields(raw)
	if len(parts) == 0 {
		return []string{"sudo", "/bin/systemctl", "reload", "haproxy"}
	}
	return parts
}

// projectBaseDir returns the directory the process was started from, used as
// the root for resolving default playbook and asset paths. It falls back to "."
// when the working directory cannot be determined.
func projectBaseDir() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}
