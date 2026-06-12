package dbmanager

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/lib/pq"
)

// Service manages PostgreSQL users and databases on a running cluster.
// It is stateless; each method opens its own connections.
type Service struct{}

func NewService() *Service { return &Service{} }

// CreateUser creates (or updates) a login role with full DML + DDL
// privileges on every existing non-system database:
//
//   - Role attributes: LOGIN CREATEDB NOSUPERUSER NOCREATEROLE
//     → can create and own new databases, cannot manage other roles
//   - Per-database: GRANT ALL PRIVILEGES ON DATABASE
//   - Per-schema: GRANT USAGE, CREATE ON SCHEMA public
//     → can create tables (owns them, so can drop them)
//   - Per-schema: GRANT SELECT, INSERT, UPDATE, DELETE, TRUNCATE
//     ON ALL TABLES + ALL SEQUENCES
//   - ALTER DEFAULT PRIVILEGES so objects created by any existing user
//     are automatically accessible to the new user and vice-versa
func (s *Service) CreateUser(ctx context.Context, req CreateUserRequest) error {
	if err := req.validate(); err != nil {
		return err
	}

	root, err := s.connect(ctx, req.PrimaryIP, req.Port, "postgres", req.AdminUser, req.AdminPassword)
	if err != nil {
		return err
	}
	defer root.Close()

	// Create role or refresh password (idempotent).
	userLit := pq.QuoteLiteral(req.Username)
	passLit := pq.QuoteLiteral(req.Password)
	upsertRole := fmt.Sprintf(`
		DO $body$
		BEGIN
		  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = %s) THEN
		    EXECUTE format(
		      'CREATE ROLE %%I WITH LOGIN CREATEDB NOSUPERUSER NOCREATEROLE INHERIT PASSWORD %%L',
		      %s, %s);
		  ELSE
		    EXECUTE format(
		      'ALTER ROLE %%I WITH LOGIN CREATEDB NOSUPERUSER NOCREATEROLE INHERIT PASSWORD %%L',
		      %s, %s);
		  END IF;
		END
		$body$`, userLit, userLit, passLit, userLit, passLit)
	if _, err := root.ExecContext(ctx, upsertRole); err != nil {
		return fmt.Errorf("upsert role: %w", err)
	}

	dbs, err := appDatabases(ctx, root)
	if err != nil {
		return fmt.Errorf("list databases: %w", err)
	}
	// Snapshot of existing app users before the new one is included; used to
	// set bidirectional ALTER DEFAULT PRIVILEGES.
	existingUsers, err := appUsers(ctx, root)
	if err != nil {
		return fmt.Errorf("list users: %w", err)
	}
	// The upsert above may have just created the user; exclude it from the
	// "existing" set so we don't try to grant it to itself.
	peers := without(existingUsers, req.Username)

	uid := pq.QuoteIdentifier(req.Username)
	for _, dbname := range dbs {
		if _, err := root.ExecContext(ctx,
			"GRANT ALL PRIVILEGES ON DATABASE "+pq.QuoteIdentifier(dbname)+" TO "+uid,
		); err != nil {
			return fmt.Errorf("grant on database %q: %w", dbname, err)
		}
		if err := s.grantInDatabase(ctx, req.PrimaryIP, req.Port, dbname,
			req.AdminUser, req.AdminPassword, req.Username, peers); err != nil {
			return err
		}
	}
	return nil
}

// DeleteUser revokes all privileges, reassigns owned objects to the admin,
// then drops the role.  Superusers, replication roles and pg_* system roles
// are rejected to prevent accidental cluster damage.
func (s *Service) DeleteUser(ctx context.Context, req DeleteUserRequest) error {
	if err := req.validate(); err != nil {
		return err
	}

	root, err := s.connect(ctx, req.PrimaryIP, req.Port, "postgres", req.AdminUser, req.AdminPassword)
	if err != nil {
		return err
	}
	defer root.Close()

	var rolName string
	var rolSuper, rolReplication bool
	err = root.QueryRowContext(ctx,
		`SELECT rolname, rolsuper, rolreplication
		   FROM pg_roles WHERE rolname = $1`, req.Username,
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

	// Per-database cleanup must happen before DROP ROLE.
	for _, dbname := range dbs {
		if err := s.revokeInDatabase(ctx, req.PrimaryIP, req.Port, dbname,
			req.AdminUser, req.AdminPassword, req.Username); err != nil {
			return err
		}
	}

	if _, err := root.ExecContext(ctx,
		"DROP ROLE "+pq.QuoteIdentifier(req.Username),
	); err != nil {
		return fmt.Errorf("drop role: %w", err)
	}
	return nil
}

// CreateDatabase creates a database (owned by owner, defaulting to admin_user)
// then grants every existing non-system user full access: CONNECT, schema
// CREATE/USAGE, and DML on all present and future tables.
func (s *Service) CreateDatabase(ctx context.Context, req CreateDatabaseRequest) error {
	if err := req.validate(); err != nil {
		return err
	}

	root, err := s.connect(ctx, req.PrimaryIP, req.Port, "postgres", req.AdminUser, req.AdminPassword)
	if err != nil {
		return err
	}
	defer root.Close()

	owner := req.Owner
	if owner == "" {
		owner = req.AdminUser
	}

	dbid := pq.QuoteIdentifier(req.DBName)
	// CREATE DATABASE cannot run inside a transaction; sql.DB.ExecContext uses
	// autocommit when not inside an explicit Begin(), so this is safe.
	if _, err := root.ExecContext(ctx,
		"CREATE DATABASE "+dbid+" OWNER "+pq.QuoteIdentifier(owner),
	); err != nil {
		return fmt.Errorf("create database: %w", err)
	}

	users, err := appUsers(ctx, root)
	if err != nil {
		return fmt.Errorf("list users: %w", err)
	}

	for _, u := range users {
		if _, err := root.ExecContext(ctx,
			"GRANT ALL PRIVILEGES ON DATABASE "+dbid+" TO "+pq.QuoteIdentifier(u),
		); err != nil {
			return fmt.Errorf("grant on database %q to %q: %w", req.DBName, u, err)
		}
	}

	// Inside the new database, grant each user full schema access and wire up
	// bidirectional ALTER DEFAULT PRIVILEGES between all user pairs.
	for _, u := range users {
		peers := without(users, u)
		if err := s.grantInDatabase(ctx, req.PrimaryIP, req.Port, req.DBName,
			req.AdminUser, req.AdminPassword, u, peers); err != nil {
			return err
		}
	}
	return nil
}

// DeleteDatabase terminates all connections to the database, then drops it.
// The three PostgreSQL system databases are refused.
func (s *Service) DeleteDatabase(ctx context.Context, req DeleteDatabaseRequest) error {
	if err := req.validate(); err != nil {
		return err
	}
	switch req.DBName {
	case "postgres", "template0", "template1":
		return fmt.Errorf("database %q is a system database and cannot be deleted", req.DBName)
	}

	root, err := s.connect(ctx, req.PrimaryIP, req.Port, "postgres", req.AdminUser, req.AdminPassword)
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

	// DROP DATABASE also cannot run inside a transaction.
	if _, err := root.ExecContext(ctx,
		"DROP DATABASE "+pq.QuoteIdentifier(req.DBName),
	); err != nil {
		return fmt.Errorf("drop database: %w", err)
	}
	return nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

func (s *Service) connect(ctx context.Context, host string, port int, dbname, user, password string) (*sql.DB, error) {
	dsn := fmt.Sprintf("host=%s port=%d dbname=%s user=%s password=%s sslmode=require",
		host, port, pgConnVal(dbname), pgConnVal(user), pgConnVal(password))
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

// pgConnVal single-quotes a connection-string value, escaping backslashes and
// single quotes per the libpq keyword=value format.
func pgConnVal(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `'`, `\'`)
	return "'" + v + "'"
}

// appDatabases returns all non-template databases except the 'postgres' system db.
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

// appUsers returns all login-capable, non-superuser, non-replication,
// non-system roles (excludes pg_* built-ins and the 'postgres' superuser).
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

// grantInDatabase opens a connection to dbname and:
//  1. Grants USAGE + CREATE on schema public (lets the user create tables)
//  2. Grants SELECT/INSERT/UPDATE/DELETE/TRUNCATE on all existing tables
//  3. Grants ALL on all existing sequences
//  4. Sets ALTER DEFAULT PRIVILEGES so future objects by admin are accessible
//  5. Sets bidirectional ALTER DEFAULT PRIVILEGES between targetUser and each peer
//     so tables created by either party are automatically accessible to the other
func (s *Service) grantInDatabase(ctx context.Context, host string, port int,
	dbname, adminUser, adminPassword, targetUser string, peers []string) error {

	db, err := s.connect(ctx, host, port, dbname, adminUser, adminPassword)
	if err != nil {
		return err
	}
	defer db.Close()

	target := pq.QuoteIdentifier(targetUser)

	// Direct grants on existing objects.
	for _, stmt := range []string{
		"GRANT USAGE ON SCHEMA public TO " + target,
		"GRANT CREATE ON SCHEMA public TO " + target,
		"GRANT SELECT, INSERT, UPDATE, DELETE, TRUNCATE ON ALL TABLES IN SCHEMA public TO " + target,
		"GRANT ALL ON ALL SEQUENCES IN SCHEMA public TO " + target,
		// Objects that admin creates in the future are accessible to target.
		"ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE, TRUNCATE ON TABLES TO " + target,
		"ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON SEQUENCES TO " + target,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("db %q: %s: %w", dbname, stmt, err)
		}
	}

	// Bidirectional default privileges between target and every peer.
	// Failures are best-effort (pg_default_acl updates may already exist).
	for _, peer := range peers {
		pid := pq.QuoteIdentifier(peer)
		for _, stmt := range []string{
			// peer creates table → target can use it
			fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE, TRUNCATE ON TABLES TO %s", pid, target),
			fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public GRANT ALL ON SEQUENCES TO %s", pid, target),
			// target creates table → peer can use it
			fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE, TRUNCATE ON TABLES TO %s", target, pid),
			fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public GRANT ALL ON SEQUENCES TO %s", target, pid),
		} {
			_, _ = db.ExecContext(ctx, stmt)
		}
	}
	return nil
}

// revokeInDatabase reassigns all objects owned by targetUser to the admin and
// drops any remaining privileges, cleaning up one database before DROP ROLE.
func (s *Service) revokeInDatabase(ctx context.Context, host string, port int,
	dbname, adminUser, adminPassword, targetUser string) error {

	db, err := s.connect(ctx, host, port, dbname, adminUser, adminPassword)
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

// without returns a copy of slice with target removed.
func without(slice []string, target string) []string {
	out := make([]string, 0, len(slice))
	for _, v := range slice {
		if v != target {
			out = append(out, v)
		}
	}
	return out
}
