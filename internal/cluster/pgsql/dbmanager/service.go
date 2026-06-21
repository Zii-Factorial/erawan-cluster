package dbmanager

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	pgsql "erawan-cluster/internal/cluster/pgsql"
	"github.com/lib/pq"
)

type Service struct {
	store      *pgsql.Store
	httpClient *http.Client
}

func NewService(store *pgsql.Store) *Service {
	return &Service{
		store:      store,
		httpClient: &http.Client{Timeout: 4 * time.Second},
	}
}

// resolve loads the current primary IP, port, and postgres superuser credentials.
// It probes each node's Patroni /master endpoint so the result is correct even
// after a failover since the original deploy.
func (s *Service) resolve(ctx context.Context, jobID string) (host string, port int, user, password string, err error) {
	job, err := s.store.Load(jobID)
	if err != nil {
		return "", 0, "", "", fmt.Errorf("load job %q: %w", jobID, err)
	}
	secret, err := s.store.LoadSecret(jobID)
	if err != nil {
		return "", 0, "", "", fmt.Errorf("load job secret %q: %w", jobID, err)
	}
	p := job.Request.PostgresPort
	if p == 0 {
		p = 5432
	}
	candidates := append([]string{job.Request.PrimaryIP}, job.Request.StandbyIPs...)
	primary, err := s.findPrimary(ctx, candidates)
	if err != nil {
		return "", 0, "", "", fmt.Errorf("discover primary: %w", err)
	}
	return primary, p, secret.PostgresUser, secret.PostgresPassword, nil
}

// findPrimary probes each node's Patroni /master endpoint (port 8008) and
// returns the first node that responds 200 — that is the current read-write leader.
func (s *Service) findPrimary(ctx context.Context, candidates []string) (string, error) {
	for _, ip := range candidates {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			fmt.Sprintf("http://%s:8008/master", ip), nil)
		if err != nil {
			continue
		}
		resp, err := s.httpClient.Do(req)
		if err != nil {
			continue
		}
		status := resp.StatusCode
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if status == http.StatusOK {
			return ip, nil
		}
	}
	return "", fmt.Errorf("no primary found among nodes %v", candidates)
}

func (s *Service) CreateUser(ctx context.Context, req CreateUserRequest) error {
	if err := req.validate(); err != nil {
		return err
	}
	host, port, adminUser, adminPass, err := s.resolve(ctx, req.JobID)
	if err != nil {
		return err
	}

	root, err := s.connect(ctx, host, port, "postgres", adminUser, adminPass)
	if err != nil {
		return err
	}
	defer root.Close()

	userLit := pq.QuoteLiteral(req.Username)
	passLit := pq.QuoteLiteral(req.Password)
	createdbOpt := "CREATEDB"
	if req.DatabaseName != "" {
		createdbOpt = "NOCREATEDB"
	}
	upsertRole := fmt.Sprintf(`
		DO $body$
		BEGIN
		  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = %s) THEN
		    EXECUTE format(
		      'CREATE ROLE %%I WITH LOGIN %s NOSUPERUSER NOCREATEROLE INHERIT PASSWORD %%L',
		      %s, %s);
		  ELSE
		    EXECUTE format(
		      'ALTER ROLE %%I WITH LOGIN %s NOSUPERUSER NOCREATEROLE INHERIT PASSWORD %%L',
		      %s, %s);
		  END IF;
		END
		$body$`, userLit, createdbOpt, userLit, passLit, createdbOpt, userLit, passLit)
	if _, err := root.ExecContext(ctx, upsertRole); err != nil {
		return fmt.Errorf("upsert role: %w", err)
	}

	uid := pq.QuoteIdentifier(req.Username)

	if req.DatabaseName != "" {
		existingUsers, err := appUsers(ctx, root)
		if err != nil {
			return fmt.Errorf("list users: %w", err)
		}
		peers := without(existingUsers, req.Username)

		var dbExists bool
		if err := root.QueryRowContext(ctx,
			"SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)", req.DatabaseName,
		).Scan(&dbExists); err != nil {
			return fmt.Errorf("check database: %w", err)
		}
		if !dbExists {
			if _, err := root.ExecContext(ctx,
				"CREATE DATABASE "+pq.QuoteIdentifier(req.DatabaseName)+" OWNER "+pq.QuoteIdentifier(adminUser),
			); err != nil {
				return fmt.Errorf("create database: %w", err)
			}
		}
		if _, err := root.ExecContext(ctx,
			"GRANT ALL PRIVILEGES ON DATABASE "+pq.QuoteIdentifier(req.DatabaseName)+" TO "+uid,
		); err != nil {
			return fmt.Errorf("grant on database %q: %w", req.DatabaseName, err)
		}
		if err := s.grantInDatabase(ctx, host, port, req.DatabaseName, adminUser, adminPass, req.Username, peers); err != nil {
			return err
		}
	}

	return nil
}

func (s *Service) ResetPassword(ctx context.Context, req ResetPasswordRequest) error {
	if err := req.validate(); err != nil {
		return err
	}
	host, port, adminUser, adminPass, err := s.resolve(ctx, req.JobID)
	if err != nil {
		return err
	}

	root, err := s.connect(ctx, host, port, "postgres", adminUser, adminPass)
	if err != nil {
		return err
	}
	defer root.Close()

	var rolName string
	err = root.QueryRowContext(ctx,
		`SELECT rolname FROM pg_roles WHERE rolname = $1`,
		req.Username,
	).Scan(&rolName)
	if err == sql.ErrNoRows {
		return fmt.Errorf("user %q does not exist", req.Username)
	}
	if err != nil {
		return fmt.Errorf("lookup role: %w", err)
	}
	if rolName == "postgres" || strings.HasPrefix(rolName, "pg_") {
		return fmt.Errorf("user %q is a protected system role and cannot be modified", req.Username)
	}

	if _, err := root.ExecContext(ctx,
		"ALTER ROLE "+pq.QuoteIdentifier(req.Username)+" WITH PASSWORD "+pq.QuoteLiteral(req.Password),
	); err != nil {
		return fmt.Errorf("reset password: %w", err)
	}
	return nil
}

func (s *Service) UpdateUser(ctx context.Context, req UpdateUserRequest) error {
	if err := req.validate(); err != nil {
		return err
	}
	host, port, adminUser, adminPass, err := s.resolve(ctx, req.JobID)
	if err != nil {
		return err
	}

	root, err := s.connect(ctx, host, port, "postgres", adminUser, adminPass)
	if err != nil {
		return err
	}
	defer root.Close()

	var rolName string
	err = root.QueryRowContext(ctx,
		`SELECT rolname FROM pg_roles WHERE rolname = $1`,
		req.Username,
	).Scan(&rolName)
	if err == sql.ErrNoRows {
		return fmt.Errorf("user %q does not exist", req.Username)
	}
	if err != nil {
		return fmt.Errorf("lookup role: %w", err)
	}
	if rolName == "postgres" || strings.HasPrefix(rolName, "pg_") {
		return fmt.Errorf("user %q is a protected system role and cannot be renamed", req.Username)
	}

	if _, err := root.ExecContext(ctx,
		"ALTER ROLE "+pq.QuoteIdentifier(req.Username)+" RENAME TO "+pq.QuoteIdentifier(req.NewUsername),
	); err != nil {
		return fmt.Errorf("rename role: %w", err)
	}
	return nil
}

func (s *Service) DeleteUser(ctx context.Context, req DeleteUserRequest) error {
	if err := req.validate(); err != nil {
		return err
	}
	host, port, adminUser, adminPass, err := s.resolve(ctx, req.JobID)
	if err != nil {
		return err
	}

	root, err := s.connect(ctx, host, port, "postgres", adminUser, adminPass)
	if err != nil {
		return err
	}
	defer root.Close()

	var rolName string
	var rolSuper, rolReplication bool
	err = root.QueryRowContext(ctx,
		`SELECT rolname, rolsuper, rolreplication FROM pg_roles WHERE rolname = $1`,
		req.Username,
	).Scan(&rolName, &rolSuper, &rolReplication)
	if err == sql.ErrNoRows {
		return fmt.Errorf("user %q does not exist", req.Username)
	}
	if err != nil {
		return fmt.Errorf("lookup role: %w", err)
	}
	if rolSuper || rolReplication || strings.HasPrefix(rolName, "pg_") {
		return fmt.Errorf("user %q is a protected system role and cannot be deleted", req.Username)
	}

	dbs, err := appDatabases(ctx, root)
	if err != nil {
		return fmt.Errorf("list databases: %w", err)
	}
	for _, dbname := range dbs {
		if err := s.revokeInDatabase(ctx, host, port, dbname, adminUser, adminPass, req.Username); err != nil {
			return err
		}
	}

	if _, err := root.ExecContext(ctx, "DROP ROLE "+pq.QuoteIdentifier(req.Username)); err != nil {
		return fmt.Errorf("drop role: %w", err)
	}
	return nil
}

func (s *Service) CreateDatabase(ctx context.Context, req CreateDatabaseRequest) error {
	if err := req.validate(); err != nil {
		return err
	}
	host, port, adminUser, adminPass, err := s.resolve(ctx, req.JobID)
	if err != nil {
		return err
	}

	root, err := s.connect(ctx, host, port, "postgres", adminUser, adminPass)
	if err != nil {
		return err
	}
	defer root.Close()

	dbid := pq.QuoteIdentifier(req.DBName)
	if _, err := root.ExecContext(ctx,
		"CREATE DATABASE "+dbid+" OWNER "+pq.QuoteIdentifier(adminUser),
	); err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	return nil
}

func (s *Service) UpdateDatabase(ctx context.Context, req UpdateDatabaseRequest) error {
	if err := req.validate(); err != nil {
		return err
	}
	switch req.DBName {
	case "postgres", "template0", "template1":
		return fmt.Errorf("database %q is a system database and cannot be renamed", req.DBName)
	}

	host, port, adminUser, adminPass, err := s.resolve(ctx, req.JobID)
	if err != nil {
		return err
	}

	root, err := s.connect(ctx, host, port, "postgres", adminUser, adminPass)
	if err != nil {
		return err
	}
	defer root.Close()

	if _, err := root.ExecContext(ctx,
		`SELECT pg_terminate_backend(pid)
		   FROM pg_stat_activity
		  WHERE datname = $1 AND pid <> pg_backend_pid()`,
		req.DBName,
	); err != nil {
		return fmt.Errorf("terminate connections: %w", err)
	}

	if _, err := root.ExecContext(ctx,
		"ALTER DATABASE "+pq.QuoteIdentifier(req.DBName)+" RENAME TO "+pq.QuoteIdentifier(req.NewDBName),
	); err != nil {
		return fmt.Errorf("rename database: %w", err)
	}
	return nil
}

func (s *Service) DeleteDatabase(ctx context.Context, req DeleteDatabaseRequest) error {
	if err := req.validate(); err != nil {
		return err
	}
	switch req.DBName {
	case "postgres", "template0", "template1":
		return fmt.Errorf("database %q is a system database and cannot be deleted", req.DBName)
	}

	host, port, adminUser, adminPass, err := s.resolve(ctx, req.JobID)
	if err != nil {
		return err
	}

	root, err := s.connect(ctx, host, port, "postgres", adminUser, adminPass)
	if err != nil {
		return err
	}
	defer root.Close()

	if _, err := root.ExecContext(ctx,
		`SELECT pg_terminate_backend(pid)
		   FROM pg_stat_activity
		  WHERE datname = $1 AND pid <> pg_backend_pid()`,
		req.DBName,
	); err != nil {
		return fmt.Errorf("terminate connections: %w", err)
	}

	if _, err := root.ExecContext(ctx, "DROP DATABASE "+pq.QuoteIdentifier(req.DBName)); err != nil {
		return fmt.Errorf("drop database: %w", err)
	}
	return nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

func (s *Service) connect(ctx context.Context, host string, port int, dbname, user, password string) (*sql.DB, error) {
	// Secure by default: verify-full validates the server certificate and host
	// name (anti-MITM). Relax via CLUSTER_DB_SSL_MODE for self-signed clusters.
	dsn := fmt.Sprintf("host=%s port=%d dbname=%s user=%s password=%s sslmode=%s",
		host, port, pgConnVal(dbname), pgConnVal(user), pgConnVal(password), adminSSLMode())
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect %s:%d/%s: %w", host, port, dbname, err)
	}
	return db, nil
}

// adminSSLMode resolves the lib/pq sslmode for admin connections. Defaults to
// "verify-full"; operators may relax it via CLUSTER_DB_SSL_MODE (e.g. require,
// verify-ca, disable) for clusters using self-signed certificates.
func adminSSLMode() string {
	switch m := strings.ToLower(strings.TrimSpace(os.Getenv("CLUSTER_DB_SSL_MODE"))); m {
	case "disable", "require", "verify-ca", "verify-full", "prefer", "allow":
		return m
	default:
		return "verify-full"
	}
}

func pgConnVal(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `'`, `\'`)
	return "'" + v + "'"
}

func appDatabases(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT datname FROM pg_database
		  WHERE datistemplate = false AND datname <> 'postgres'
		  ORDER BY datname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func appUsers(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT rolname FROM pg_roles
		  WHERE rolcanlogin
		    AND NOT rolsuper
		    AND NOT rolreplication
		    AND rolname NOT LIKE 'pg_%'
		    AND rolname <> 'postgres'
		  ORDER BY rolname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func (s *Service) grantInDatabase(ctx context.Context, host string, port int,
	dbname, adminUser, adminPass, targetUser string, peers []string) error {

	db, err := s.connect(ctx, host, port, dbname, adminUser, adminPass)
	if err != nil {
		return err
	}
	defer db.Close()

	target := pq.QuoteIdentifier(targetUser)
	for _, stmt := range []string{
		"GRANT USAGE ON SCHEMA public TO " + target,
		"GRANT CREATE ON SCHEMA public TO " + target,
		"GRANT SELECT, INSERT, UPDATE, DELETE, TRUNCATE ON ALL TABLES IN SCHEMA public TO " + target,
		"GRANT ALL ON ALL SEQUENCES IN SCHEMA public TO " + target,
		"ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE, TRUNCATE ON TABLES TO " + target,
		"ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON SEQUENCES TO " + target,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("db %q: %s: %w", dbname, stmt, err)
		}
	}
	for _, peer := range peers {
		pid := pq.QuoteIdentifier(peer)
		_, _ = db.ExecContext(ctx, fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE, TRUNCATE ON TABLES TO %s", pid, target))
		_, _ = db.ExecContext(ctx, fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public GRANT ALL ON SEQUENCES TO %s", pid, target))
		_, _ = db.ExecContext(ctx, fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE, TRUNCATE ON TABLES TO %s", target, pid))
		_, _ = db.ExecContext(ctx, fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public GRANT ALL ON SEQUENCES TO %s", target, pid))
	}
	return nil
}

func (s *Service) revokeInDatabase(ctx context.Context, host string, port int,
	dbname, adminUser, adminPass, targetUser string) error {

	db, err := s.connect(ctx, host, port, dbname, adminUser, adminPass)
	if err != nil {
		return err
	}
	defer db.Close()

	target := pq.QuoteIdentifier(targetUser)
	admin := pq.QuoteIdentifier(adminUser)
	for _, stmt := range []string{
		"REASSIGN OWNED BY " + target + " TO " + admin,
		"DROP OWNED BY " + target,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("db %q: %s: %w", dbname, stmt, err)
		}
	}
	return nil
}

func without(slice []string, target string) []string {
	out := make([]string, 0, len(slice))
	for _, v := range slice {
		if v != target {
			out = append(out, v)
		}
	}
	return out
}
