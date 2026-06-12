package dbmanager

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
)

// Service manages MySQL users and databases on a running InnoDB Cluster.
// It is stateless; each method opens its own connection to the primary.
type Service struct{}

func NewService() *Service { return &Service{} }

// CreateUser creates (or updates) a MySQL user with the following grants:
//
//   - Global privileges ON *.*: SELECT, INSERT, UPDATE, DELETE, CREATE, DROP,
//     ALTER, INDEX, REFERENCES
//     → full DML; can CREATE/DROP tables and databases; can CREATE/DROP INDEX
//   - NOT granted: CREATE USER, SUPER, GRANT OPTION, REPLICATION SLAVE
//     → cannot manage other users, cannot escalate privileges
//
// Because the grant is global (ON *.*), the user automatically has access to
// every existing and future database — satisfying the "all databases shared"
// requirement without per-database re-grants.
//
// The operation is idempotent: re-running it refreshes the password and
// re-applies grants.
func (s *Service) CreateUser(ctx context.Context, req CreateUserRequest) error {
	if err := req.validate(); err != nil {
		return err
	}

	db, err := s.connect(ctx, req.PrimaryIP, req.Port, "", req.AdminUser, req.AdminPassword)
	if err != nil {
		return err
	}
	defer db.Close()

	uid := mysqlID(req.Username)

	// Create the account if it does not exist yet.
	if _, err := db.ExecContext(ctx,
		fmt.Sprintf("CREATE USER IF NOT EXISTS %s@'%%' IDENTIFIED WITH caching_sha2_password BY ?", uid),
		req.Password,
	); err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	// Always refresh the password (covers re-runs / password rotation).
	if _, err := db.ExecContext(ctx,
		fmt.Sprintf("ALTER USER %s@'%%' IDENTIFIED WITH caching_sha2_password BY ?", uid),
		req.Password,
	); err != nil {
		return fmt.Errorf("update password: %w", err)
	}

	grantSQL := fmt.Sprintf(
		"GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, ALTER, INDEX, REFERENCES ON *.* TO %s@'%%'",
		uid)
	if _, err := db.ExecContext(ctx, grantSQL); err != nil {
		return fmt.Errorf("grant privileges: %w", err)
	}
	if _, err := db.ExecContext(ctx, "FLUSH PRIVILEGES"); err != nil {
		return fmt.Errorf("flush privileges: %w", err)
	}
	return nil
}

// DeleteUser drops a MySQL user after verifying it is not a system account or
// a superuser.
func (s *Service) DeleteUser(ctx context.Context, req DeleteUserRequest) error {
	if err := req.validate(); err != nil {
		return err
	}

	db, err := s.connect(ctx, req.PrimaryIP, req.Port, "mysql", req.AdminUser, req.AdminPassword)
	if err != nil {
		return err
	}
	defer db.Close()

	var superPriv string
	err = db.QueryRowContext(ctx,
		"SELECT Super_priv FROM mysql.user WHERE User = ? AND Host = '%'",
		req.Username,
	).Scan(&superPriv)
	if err == sql.ErrNoRows {
		return fmt.Errorf("user %q does not exist", req.Username)
	}
	if err != nil {
		return fmt.Errorf("lookup user: %w", err)
	}
	if systemUsers[req.Username] {
		return fmt.Errorf("user %q is a protected system user and cannot be deleted", req.Username)
	}
	if superPriv == "Y" {
		return fmt.Errorf("user %q has SUPER privilege and cannot be deleted through this API", req.Username)
	}

	if _, err := db.ExecContext(ctx,
		fmt.Sprintf("DROP USER IF EXISTS %s@'%%'", mysqlID(req.Username)),
	); err != nil {
		return fmt.Errorf("drop user: %w", err)
	}
	if _, err := db.ExecContext(ctx, "FLUSH PRIVILEGES"); err != nil {
		return fmt.Errorf("flush privileges: %w", err)
	}
	return nil
}

// CreateDatabase creates a database and adds an explicit per-database grant
// for every existing non-system user so that SHOW GRANTS reflects the access.
// (Those users already have ON *.* global grants, so the per-db grant is
// additive for visibility only.)
func (s *Service) CreateDatabase(ctx context.Context, req CreateDatabaseRequest) error {
	if err := req.validate(); err != nil {
		return err
	}

	db, err := s.connect(ctx, req.PrimaryIP, req.Port, "", req.AdminUser, req.AdminPassword)
	if err != nil {
		return err
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx,
		"CREATE DATABASE "+mysqlID(req.DBName),
	); err != nil {
		return fmt.Errorf("create database: %w", err)
	}

	users, err := appUsers(ctx, db)
	if err != nil {
		return fmt.Errorf("list users: %w", err)
	}
	dbid := mysqlID(req.DBName)
	for _, u := range users {
		grantSQL := fmt.Sprintf(
			"GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, ALTER, INDEX, REFERENCES ON %s.* TO %s@'%%'",
			dbid, mysqlID(u))
		if _, err := db.ExecContext(ctx, grantSQL); err != nil {
			return fmt.Errorf("grant on %s to %s: %w", req.DBName, u, err)
		}
	}
	if _, err := db.ExecContext(ctx, "FLUSH PRIVILEGES"); err != nil {
		return fmt.Errorf("flush privileges: %w", err)
	}
	return nil
}

// DeleteDatabase terminates all active connections to the target database, then
// drops it.  The four MySQL system databases are refused.
func (s *Service) DeleteDatabase(ctx context.Context, req DeleteDatabaseRequest) error {
	if err := req.validate(); err != nil {
		return err
	}
	if systemDatabases[req.DBName] {
		return fmt.Errorf("database %q is a system database and cannot be deleted", req.DBName)
	}

	db, err := s.connect(ctx, req.PrimaryIP, req.Port, "", req.AdminUser, req.AdminPassword)
	if err != nil {
		return err
	}
	defer db.Close()

	// Collect connection IDs for the target database, excluding our own.
	rows, err := db.QueryContext(ctx,
		"SELECT id FROM information_schema.PROCESSLIST WHERE db = ? AND id != CONNECTION_ID()",
		req.DBName)
	if err != nil {
		return fmt.Errorf("list connections: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if scanErr := rows.Scan(&id); scanErr != nil {
			rows.Close()
			return fmt.Errorf("scan connection id: %w", scanErr)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list connections: %w", err)
	}
	for _, id := range ids {
		_, _ = db.ExecContext(ctx, fmt.Sprintf("KILL %d", id))
	}

	if _, err := db.ExecContext(ctx,
		"DROP DATABASE "+mysqlID(req.DBName),
	); err != nil {
		return fmt.Errorf("drop database: %w", err)
	}
	return nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

func (s *Service) connect(ctx context.Context, host string, port int, dbname, user, password string) (*sql.DB, error) {
	cfg := gomysql.NewConfig()
	cfg.User = user
	cfg.Passwd = password
	cfg.Net = "tcp"
	cfg.Addr = fmt.Sprintf("%s:%d", host, port)
	cfg.DBName = dbname
	cfg.Timeout = 10 * time.Second
	cfg.ReadTimeout = 30 * time.Second
	cfg.WriteTimeout = 30 * time.Second
	cfg.ParseTime = true
	cfg.AllowNativePasswords = true
	cfg.TLSConfig = "preferred"

	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect %s:%d: %w", host, port, err)
	}
	return db, nil
}

// mysqlID wraps a name in backtick quotes, doubling any internal backticks.
func mysqlID(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

var systemDatabases = map[string]bool{
	"information_schema": true,
	"mysql":              true,
	"performance_schema": true,
	"sys":                true,
}

var systemUsers = map[string]bool{
	"root":             true,
	"mysql.sys":        true,
	"mysql.session":    true,
	"mysql.infoschema": true,
}

// appDatabases returns all non-system databases.
func appDatabases(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT schema_name FROM information_schema.schemata
		  WHERE schema_name NOT IN ('information_schema','mysql','performance_schema','sys')
		  ORDER BY schema_name`)
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

// appUsers returns all non-system, non-superuser users that connect from any host (%).
func appUsers(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT User FROM mysql.user
		  WHERE Host = '%'
		    AND User NOT IN ('root','mysql.sys','mysql.session','mysql.infoschema')
		    AND Super_priv = 'N'
		    AND User != ''
		  ORDER BY User`)
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
