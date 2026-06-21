package main

import (
	"context"
	"fmt"
	"log"

	mysqlcluster "erawan-cluster/internal/cluster/mysql"
	mysqldbmanager "erawan-cluster/internal/cluster/mysql/dbmanager"
	pgsqlcluster "erawan-cluster/internal/cluster/pgsql"
	"erawan-cluster/internal/cluster/pgsql/dbmanager"
	"erawan-cluster/internal/haproxy"
	"erawan-cluster/internal/security"
)

// buildApplication wires every subsystem from resolved configuration and
// returns an application ready to serve HTTP. It fails fast: if any dependency
// cannot be initialised it returns an error (wrapped with context) so main can
// abort start-up cleanly instead of running half-configured.
//
// This is the single place where the engines are assembled. Adding a new engine
// (Redis, MongoDB, MariaDB, ...) means: load its clusterEngineConfig in
// config.go, add a buildXCluster helper below, and attach the resulting service
// to the application here — no change to main's control flow.
func buildApplication(ctx context.Context, cfg runtimeConfig) (*application, error) {
	if err := validateSecurityConfig(cfg); err != nil {
		return nil, err
	}

	haproxySvc, err := buildHAProxy(cfg.haproxy)
	if err != nil {
		return nil, err
	}

	mysqlStore, mysqlSvc, err := buildMySQLCluster(ctx, cfg.mysql, cfg.ssh, cfg.maxConcurrentJobs)
	if err != nil {
		return nil, err
	}

	pgsqlStore, pgsqlSvc, err := buildPGSQLCluster(ctx, cfg.pgsql, cfg.ssh, cfg.maxConcurrentJobs)
	if err != nil {
		return nil, err
	}

	cipher, err := buildCipher(cfg.encryptionKey)
	if err != nil {
		return nil, err
	}

	return &application{
		config:       cfg.server,
		haproxy:      haproxySvc,
		mysqlCluster: mysqlSvc,
		pgsqlCluster: pgsqlSvc,
		pgsqlDB:      dbmanager.NewService(pgsqlStore),
		mysqlDB:      mysqldbmanager.NewService(mysqlStore),
		cipher:       cipher,
		baseDir:      cfg.baseDir,
		enablePprof:  cfg.enablePprof,
	}, nil
}

// validateSecurityConfig fails start-up closed when the API is unauthenticated
// outside development (see security.ValidateAuthConfig).
func validateSecurityConfig(cfg runtimeConfig) error {
	return security.ValidateAuthConfig(cfg.server.env, cfg.server.apiKey)
}

// buildHAProxy constructs the HAProxy service that renders per-tenant config
// fragments and reloads the proxy. It applies the optional list of operator
// "main" config files that tenant operations must never touch.
func buildHAProxy(cfg haproxyConfig) (*haproxy.Service, error) {
	svc, err := haproxy.NewService(cfg.tenantsDir, cfg.reloadCmd, cfg.reloadTimeout)
	if err != nil {
		return nil, fmt.Errorf("init haproxy service: %w", err)
	}
	if len(cfg.mainConfigs) > 0 {
		svc.SetMainConfigs(cfg.mainConfigs)
	}
	return svc, nil
}

// buildMySQLCluster assembles the MySQL cluster engine: a persistent job store,
// an Ansible runner bound to the MySQL playbooks, and the service that ties
// them together. It returns the store as well as the service because the store
// is reused by the MySQL database manager (users/databases) wired in
// buildApplication.
//
// The store's stale jobs are marked failed on boot so that a crash mid-deploy
// does not leave jobs stuck in a perpetual "running" state.
func buildMySQLCluster(ctx context.Context, cfg clusterEngineConfig, ssh sshConfig, maxConcurrentJobs int) (*mysqlcluster.Store, *mysqlcluster.Service, error) {
	store, err := mysqlcluster.NewStore(cfg.stateDir)
	if err != nil {
		return nil, nil, fmt.Errorf("init mysql cluster store: %w", err)
	}
	store.MarkStaleRunningJobsFailed()

	runner := mysqlcluster.NewRunner(cfg.ansible.bin, cfg.deployPlaybook, cfg.rollbackPlaybook)
	runner.SetAddMemberPlaybook(cfg.addMemberPlaybook)
	runner.SetRemoveMemberPlaybook(cfg.removeMemberPlaybook)
	runner.SetDebug(cfg.ansible.verbosity, cfg.ansible.debug, cfg.ansible.stepOutputMaxChars)
	runner.SetSSHPolicy(ssh.policy())

	svc := mysqlcluster.NewService(store, runner)
	svc.SetMaxConcurrentJobs(maxConcurrentJobs)
	svc.SetContext(ctx)
	if err := applySSHConfig(ssh, svc.SetSSHConfig); err != nil {
		return nil, nil, fmt.Errorf("init mysql ssh config: %w", err)
	}

	logAnsibleDebug("mysql", cfg.ansible)
	return store, svc, nil
}

/**
*
 */
// buildPGSQLCluster assembles the PostgreSQL cluster engine. It mirrors
// buildMySQLCluster but its runner has no rollback playbook, because the
// PostgreSQL deploy flow does not support rollback. The store is returned for
// reuse by the PostgreSQL database manager.
func buildPGSQLCluster(ctx context.Context, cfg clusterEngineConfig, ssh sshConfig, maxConcurrentJobs int) (*pgsqlcluster.Store, *pgsqlcluster.Service, error) {
	store, err := pgsqlcluster.NewStore(cfg.stateDir)
	if err != nil {
		return nil, nil, fmt.Errorf("init pgsql cluster store: %w", err)
	}
	store.MarkStaleRunningJobsFailed()

	runner := pgsqlcluster.NewRunner(cfg.ansible.bin, cfg.deployPlaybook)
	runner.SetAddMemberPlaybook(cfg.addMemberPlaybook)
	runner.SetRemoveMemberPlaybook(cfg.removeMemberPlaybook)
	runner.SetDebug(cfg.ansible.verbosity, cfg.ansible.debug, cfg.ansible.stepOutputMaxChars)
	runner.SetSSHPolicy(ssh.policy())

	svc := pgsqlcluster.NewService(store, runner)
	svc.SetMaxConcurrentJobs(maxConcurrentJobs)
	svc.SetContext(ctx)
	if err := applySSHConfig(ssh, svc.SetSSHConfig); err != nil {
		return nil, nil, fmt.Errorf("init pgsql ssh config: %w", err)
	}

	logAnsibleDebug("pgsql", cfg.ansible)
	return store, svc, nil
}

// buildCipher constructs the AES-256-GCM cipher used by the encrypt/decrypt
// middleware to protect request and response payloads. Encryption is optional:
// when no key is configured the function returns a nil cipher and the
// middleware degrades to a pass-through.
func buildCipher(key string) (*security.Cipher, error) {
	if key == "" {
		return nil, nil
	}
	cipher, err := security.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("init encryption cipher: %w", err)
	}
	log.Printf("payload encryption enabled (AES-256-GCM)")
	return cipher, nil
}

// applySSHConfig pushes the shared SSH credentials into a cluster service, but
// only when credentials were actually provided. The setter differs per engine,
// so it is passed in as a function; this keeps the "only set if present" guard
// in one place instead of duplicated in every builder.
func applySSHConfig(ssh sshConfig, set func(user, keyPath string) error) error {
	if !ssh.hasCredentials() {
		return nil
	}
	return set(ssh.user, ssh.privateKeyPath)
}

// logAnsibleDebug emits a one-line summary of the Ansible debug settings for an
// engine, but only when debugging is enabled — so normal runs stay quiet.
func logAnsibleDebug(engine string, cfg ansibleConfig) {
	if !cfg.debug {
		return
	}
	log.Printf("%s ansible debug enabled: verbosity=%d, step_output_max_chars=%d",
		engine, cfg.verbosity, cfg.stepOutputMaxChars)
}
