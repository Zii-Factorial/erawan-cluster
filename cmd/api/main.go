package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	mysqlcluster "erawan-cluster/internal/cluster/mysql"
	pgsqlcluster "erawan-cluster/internal/cluster/pgsql"
	"erawan-cluster/internal/env"
	"erawan-cluster/internal/haproxy"
	"erawan-cluster/internal/security"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	addr := env.GetString("API_ADDR", "")
	if strings.TrimSpace(addr) == "" {
		host := env.GetString("API_HOST", "0.0.0.0")
		port := env.GetString("API_PORT", "8080")
		addr = host + ":" + port
	}

	baseDir := projectBaseDir()
	tenantsDir := env.GetString("TENANTS_DIR", "/var/lib/erawan-cluster/haproxy/tenants")
	reloadCmd := parseCommand(env.GetString("HAPROXY_RELOAD_CMD", "sudo /bin/systemctl reload haproxy"))
	reloadTimeoutSeconds := env.GetInt("HAPROXY_RELOAD_TIMEOUT_SECONDS", 15)

	haproxySvc, err := haproxy.NewService(tenantsDir, reloadCmd, time.Duration(reloadTimeoutSeconds)*time.Second)
	if err != nil {
		log.Fatalf("init haproxy service: %v", err)
	}

	stateDir := env.GetString("CLUSTER_STATE_DIR", "/var/lib/erawan-cluster/cluster/jobs")
	store, err := mysqlcluster.NewStore(stateDir)
	if err != nil {
		log.Fatalf("init mysql cluster store: %v", err)
	}
	store.MarkStaleRunningJobsFailed()
	pgsqlStore, err := pgsqlcluster.NewStore(env.GetString("PGSQL_CLUSTER_STATE_DIR", filepath.Join(stateDir, "pgsql")))
	if err != nil {
		log.Fatalf("init pgsql cluster store: %v", err)
	}
	pgsqlStore.MarkStaleRunningJobsFailed()

	ansibleBin := env.GetString("ANSIBLE_PLAYBOOK_BIN", "ansible-playbook")
	clusterSSHUser := env.GetString("CLUSTER_SSH_USER", "")
	clusterSSHPrivateKeyPath := env.GetString("CLUSTER_SSH_PRIVATE_KEY_PATH", "")
	deployPlaybook := env.GetString("MYSQL_DEPLOY_PLAYBOOK", filepath.Join(baseDir, "cluster/mysql/playbooks/deploy.yml"))
	rollbackPlaybook := env.GetString("MYSQL_ROLLBACK_PLAYBOOK", filepath.Join(baseDir, "cluster/mysql/playbooks/rollback.yml"))
	mysqlAnsibleDebug := env.GetBoolAny([]string{"CLUSTER_ANSIBLE_DEBUG", "MYSQL_ANSIBLE_DEBUG"}, false)
	mysqlAnsibleVerbosity := env.GetIntAny([]string{"CLUSTER_ANSIBLE_VERBOSITY", "MYSQL_ANSIBLE_VERBOSITY"}, 0)
	mysqlStepOutputMaxChars := env.GetIntAny([]string{"CLUSTER_STEP_OUTPUT_MAX_CHARS", "MYSQL_STEP_OUTPUT_MAX_CHARS"}, 8000)
	if mysqlAnsibleDebug && mysqlAnsibleVerbosity <= 0 {
		mysqlAnsibleVerbosity = 3
	}
	if mysqlAnsibleDebug && mysqlStepOutputMaxChars == 8000 {
		mysqlStepOutputMaxChars = 200000
	}
	runner := mysqlcluster.NewRunner(ansibleBin, deployPlaybook, rollbackPlaybook)
	runner.SetDebug(mysqlAnsibleVerbosity, mysqlAnsibleDebug, mysqlStepOutputMaxChars)
	mysqlSvc := mysqlcluster.NewService(store, runner)
	mysqlSvc.SetContext(ctx)
	if strings.TrimSpace(clusterSSHUser) != "" || strings.TrimSpace(clusterSSHPrivateKeyPath) != "" {
		if err := mysqlSvc.SetSSHConfig(clusterSSHUser, clusterSSHPrivateKeyPath); err != nil {
			log.Fatalf("init mysql ssh config: %v", err)
		}
	}

	pgsqlDeployPlaybook := env.GetString("PGSQL_DEPLOY_PLAYBOOK", filepath.Join(baseDir, "cluster/pgsql/playbooks/deploy.yml"))
	pgsqlAnsibleDebug := env.GetBoolAny([]string{"CLUSTER_ANSIBLE_DEBUG", "PGSQL_ANSIBLE_DEBUG"}, false)
	pgsqlAnsibleVerbosity := env.GetIntAny([]string{"CLUSTER_ANSIBLE_VERBOSITY", "PGSQL_ANSIBLE_VERBOSITY"}, 0)
	pgsqlStepOutputMaxChars := env.GetIntAny([]string{"CLUSTER_STEP_OUTPUT_MAX_CHARS", "PGSQL_STEP_OUTPUT_MAX_CHARS"}, 8000)
	if pgsqlAnsibleDebug && pgsqlAnsibleVerbosity <= 0 {
		pgsqlAnsibleVerbosity = 3
	}
	if pgsqlAnsibleDebug && pgsqlStepOutputMaxChars == 8000 {
		pgsqlStepOutputMaxChars = 200000
	}
	pgsqlRunner := pgsqlcluster.NewRunner(ansibleBin, pgsqlDeployPlaybook)
	pgsqlRunner.SetDebug(pgsqlAnsibleVerbosity, pgsqlAnsibleDebug, pgsqlStepOutputMaxChars)
	pgsqlSvc := pgsqlcluster.NewService(pgsqlStore, pgsqlRunner)
	pgsqlSvc.SetContext(ctx)
	if strings.TrimSpace(clusterSSHUser) != "" || strings.TrimSpace(clusterSSHPrivateKeyPath) != "" {
		if err := pgsqlSvc.SetSSHConfig(clusterSSHUser, clusterSSHPrivateKeyPath); err != nil {
			log.Fatalf("init pgsql ssh config: %v", err)
		}
	}

	var cipher *security.Cipher
	if encKey := env.GetString("ENCRYPTION_KEY", ""); encKey != "" {
		var err error
		cipher, err = security.NewCipher(encKey)
		if err != nil {
			log.Fatalf("init encryption cipher: %v", err)
		}
		log.Printf("payload encryption enabled (AES-256-GCM)")
	}

	app := &application{
		config: config{
			addr:    addr,
			env:     env.GetString("ENV", "dev"),
			apiKey:  env.GetString("API_KEY", ""),
			version: appVersion,
		},
		haproxy:      haproxySvc,
		mysqlCluster: mysqlSvc,
		pgsqlCluster: pgsqlSvc,
		cipher:       cipher,
		baseDir:      baseDir,
	}

	mux := app.mount()
	if mysqlAnsibleDebug {
		log.Printf("mysql ansible debug enabled: verbosity=%d, step_output_max_chars=%d", mysqlAnsibleVerbosity, mysqlStepOutputMaxChars)
	}
	if pgsqlAnsibleDebug {
		log.Printf("pgsql ansible debug enabled: verbosity=%d, step_output_max_chars=%d", pgsqlAnsibleVerbosity, pgsqlStepOutputMaxChars)
	}
	log.Printf("erawan cluster api v%s started at %s", appVersion, addr)
	if err := app.run(ctx, mux); err != nil {
		log.Fatalf("server: %v", err)
	}
}

func parseCommand(raw string) []string {
	parts := strings.Fields(raw)
	if len(parts) == 0 {
		return []string{"sudo", "/bin/systemctl", "reload", "haproxy"}
	}
	return parts
}
