package dbmanager

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	mysql "erawan-cluster/internal/cluster/mysql"
	gomysql "github.com/go-sql-driver/mysql"
)

type Service struct {
	store *mysql.Store
}

/**
 * NewService.
 *
 * Params:
 *   store *mysql.Store - the store (*mysql.Store)
 *
 * Returns:
 *   *Service - the resulting *Service
 */
func NewService(store *mysql.Store) *Service { return &Service{store: store} }

/**
 * resolve loads primary IP, port, and admin credentials from the stored job.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   jobID string - the jobID string
 *
 * Returns:
 *   host string - the host string
 *   port int - the port value
 *   user string - the user string
 *   password string - the password string
 *   err error - error value; non-nil when the operation fails
 */
func (s *Service) resolve(jobID string) (host string, port int, user, password string, err error) {
	job, err := s.store.Load(jobID)
	if err != nil {
		return "", 0, "", "", fmt.Errorf("load job %q: %w", jobID, err)
	}
	secret, err := s.store.LoadSecret(jobID)
	if err != nil {
		return "", 0, "", "", fmt.Errorf("load job secret %q: %w", jobID, err)
	}
	p := job.Request.MySQLPort
	if p == 0 {
		p = 3306
	}
	return job.Request.PrimaryIP, p, secret.AdminUser, secret.AdminPassword, nil
}

/**
 * CreateUser.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   req CreateUserRequest - the req (CreateUserRequest)
 *
 * Returns:
 *   error - error value; non-nil when the operation fails
 */
func (s *Service) CreateUser(ctx context.Context, req CreateUserRequest) error {
	if err := req.validate(); err != nil {
		return err
	}
	host, port, adminUser, adminPass, err := s.resolve(req.JobID)
	if err != nil {
		return err
	}

	db, err := s.connect(ctx, host, port, "", adminUser, adminPass)
	if err != nil {
		return err
	}
	defer db.Close()

	uid := mysqlID(req.Username)
	passLit := mysqlLit(req.Password)
	if _, err := db.ExecContext(ctx,
		fmt.Sprintf("CREATE USER IF NOT EXISTS %s@'%%' IDENTIFIED WITH caching_sha2_password BY %s", uid, passLit),
	); err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	if _, err := db.ExecContext(ctx,
		fmt.Sprintf("ALTER USER %s@'%%' IDENTIFIED WITH caching_sha2_password BY %s", uid, passLit),
	); err != nil {
		return fmt.Errorf("update password: %w", err)
	}

	if req.DatabaseName != "" {
		var exists int
		if err := db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?",
			req.DatabaseName,
		).Scan(&exists); err != nil {
			return fmt.Errorf("check database: %w", err)
		}
		if exists == 0 {
			if _, err := db.ExecContext(ctx, "CREATE DATABASE "+mysqlID(req.DatabaseName)); err != nil {
				return fmt.Errorf("create database: %w", err)
			}
		}
		if _, err := db.ExecContext(ctx, fmt.Sprintf(
			"GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, ALTER, INDEX, REFERENCES ON %s.* TO %s@'%%'",
			mysqlID(req.DatabaseName), uid),
		); err != nil {
			return fmt.Errorf("grant on database: %w", err)
		}
	}

	if _, err := db.ExecContext(ctx, "FLUSH PRIVILEGES"); err != nil {
		return fmt.Errorf("flush privileges: %w", err)
	}
	return nil
}

/**
 * ResetPassword.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   req ResetPasswordRequest - the req (ResetPasswordRequest)
 *
 * Returns:
 *   error - error value; non-nil when the operation fails
 */
func (s *Service) ResetPassword(ctx context.Context, req ResetPasswordRequest) error {
	if err := req.validate(); err != nil {
		return err
	}
	host, port, adminUser, adminPass, err := s.resolve(req.JobID)
	if err != nil {
		return err
	}

	db, err := s.connect(ctx, host, port, "mysql", adminUser, adminPass)
	if err != nil {
		return err
	}
	defer db.Close()

	var count int
	err = db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM mysql.user WHERE User = ? AND Host = '%'",
		req.Username,
	).Scan(&count)
	if err != nil {
		return fmt.Errorf("lookup user: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("user %q does not exist", req.Username)
	}
	if systemUsers[req.Username] {
		return fmt.Errorf("user %q is a protected system user", req.Username)
	}

	uid := mysqlID(req.Username)
	passLit := mysqlLit(req.Password)
	if _, err := db.ExecContext(ctx,
		fmt.Sprintf("ALTER USER %s@'%%' IDENTIFIED WITH caching_sha2_password BY %s", uid, passLit),
	); err != nil {
		return fmt.Errorf("reset password: %w", err)
	}
	if _, err := db.ExecContext(ctx, "FLUSH PRIVILEGES"); err != nil {
		return fmt.Errorf("flush privileges: %w", err)
	}
	return nil
}

/**
 * UpdateUser.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   req UpdateUserRequest - the req (UpdateUserRequest)
 *
 * Returns:
 *   error - error value; non-nil when the operation fails
 */
func (s *Service) UpdateUser(ctx context.Context, req UpdateUserRequest) error {
	if err := req.validate(); err != nil {
		return err
	}
	host, port, adminUser, adminPass, err := s.resolve(req.JobID)
	if err != nil {
		return err
	}

	db, err := s.connect(ctx, host, port, "mysql", adminUser, adminPass)
	if err != nil {
		return err
	}
	defer db.Close()

	var count int
	err = db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM mysql.user WHERE User = ? AND Host = '%'",
		req.Username,
	).Scan(&count)
	if err != nil {
		return fmt.Errorf("lookup user: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("user %q does not exist", req.Username)
	}
	if systemUsers[req.Username] {
		return fmt.Errorf("user %q is a protected system user", req.Username)
	}

	if _, err := db.ExecContext(ctx, fmt.Sprintf(
		"RENAME USER %s@'%%' TO %s@'%%'",
		mysqlID(req.Username), mysqlID(req.NewUsername)),
	); err != nil {
		return fmt.Errorf("rename user: %w", err)
	}
	if _, err := db.ExecContext(ctx, "FLUSH PRIVILEGES"); err != nil {
		return fmt.Errorf("flush privileges: %w", err)
	}
	return nil
}

/**
 * DeleteUser.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   req DeleteUserRequest - the req (DeleteUserRequest)
 *
 * Returns:
 *   error - error value; non-nil when the operation fails
 */
func (s *Service) DeleteUser(ctx context.Context, req DeleteUserRequest) error {
	if err := req.validate(); err != nil {
		return err
	}
	host, port, adminUser, adminPass, err := s.resolve(req.JobID)
	if err != nil {
		return err
	}

	db, err := s.connect(ctx, host, port, "mysql", adminUser, adminPass)
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
		return fmt.Errorf("user %q is a protected system user", req.Username)
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

/**
 * CreateDatabase.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   req CreateDatabaseRequest - the req (CreateDatabaseRequest)
 *
 * Returns:
 *   error - error value; non-nil when the operation fails
 */
func (s *Service) CreateDatabase(ctx context.Context, req CreateDatabaseRequest) error {
	if err := req.validate(); err != nil {
		return err
	}
	host, port, adminUser, adminPass, err := s.resolve(req.JobID)
	if err != nil {
		return err
	}

	db, err := s.connect(ctx, host, port, "", adminUser, adminPass)
	if err != nil {
		return err
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, "CREATE DATABASE "+mysqlID(req.DBName)); err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	return nil
}

/**
 * UpdateDatabase.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   req UpdateDatabaseRequest - the req (UpdateDatabaseRequest)
 *
 * Returns:
 *   error - error value; non-nil when the operation fails
 */
func (s *Service) UpdateDatabase(ctx context.Context, req UpdateDatabaseRequest) error {
	if err := req.validate(); err != nil {
		return err
	}
	if systemDatabases[req.DBName] {
		return fmt.Errorf("database %q is a system database and cannot be renamed", req.DBName)
	}

	host, port, adminUser, adminPass, err := s.resolve(req.JobID)
	if err != nil {
		return err
	}

	db, err := s.connect(ctx, host, port, "", adminUser, adminPass)
	if err != nil {
		return err
	}
	defer db.Close()

	var exists int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?",
		req.DBName,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check database: %w", err)
	}
	if exists == 0 {
		return fmt.Errorf("database %q does not exist", req.DBName)
	}

	if _, err := db.ExecContext(ctx, "CREATE DATABASE "+mysqlID(req.NewDBName)); err != nil {
		return fmt.Errorf("create new database: %w", err)
	}

	rows, err := db.QueryContext(ctx,
		"SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_TYPE = 'BASE TABLE'",
		req.DBName)
	if err != nil {
		return fmt.Errorf("list tables: %w", err)
	}
	var tables []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return fmt.Errorf("scan table: %w", err)
		}
		tables = append(tables, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list tables: %w", err)
	}

	oldID := mysqlID(req.DBName)
	newID := mysqlID(req.NewDBName)
	for _, t := range tables {
		tid := mysqlID(t)
		if _, err := db.ExecContext(ctx, fmt.Sprintf(
			"RENAME TABLE %s.%s TO %s.%s", oldID, tid, newID, tid),
		); err != nil {
			return fmt.Errorf("move table %s: %w", t, err)
		}
	}

	grantRows, err := db.QueryContext(ctx,
		"SELECT User FROM mysql.db WHERE Db = ? AND Host = '%'", req.DBName)
	if err != nil {
		return fmt.Errorf("list grants: %w", err)
	}
	var grantedUsers []string
	for grantRows.Next() {
		var u string
		if err := grantRows.Scan(&u); err != nil {
			grantRows.Close()
			return fmt.Errorf("scan grant: %w", err)
		}
		grantedUsers = append(grantedUsers, u)
	}
	grantRows.Close()
	if err := grantRows.Err(); err != nil {
		return fmt.Errorf("list grants: %w", err)
	}

	for _, u := range grantedUsers {
		uid := mysqlID(u)
		_, _ = db.ExecContext(ctx, fmt.Sprintf(
			"REVOKE ALL PRIVILEGES ON %s.* FROM %s@'%%'", oldID, uid))
		if _, err := db.ExecContext(ctx, fmt.Sprintf(
			"GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, ALTER, INDEX, REFERENCES ON %s.* TO %s@'%%'",
			newID, uid),
		); err != nil {
			return fmt.Errorf("grant on new database to %s: %w", u, err)
		}
	}

	if _, err := db.ExecContext(ctx, "DROP DATABASE "+oldID); err != nil {
		return fmt.Errorf("drop old database: %w", err)
	}
	if _, err := db.ExecContext(ctx, "FLUSH PRIVILEGES"); err != nil {
		return fmt.Errorf("flush privileges: %w", err)
	}
	return nil
}

/**
 * DeleteDatabase.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   req DeleteDatabaseRequest - the req (DeleteDatabaseRequest)
 *
 * Returns:
 *   error - error value; non-nil when the operation fails
 */
func (s *Service) DeleteDatabase(ctx context.Context, req DeleteDatabaseRequest) error {
	if err := req.validate(); err != nil {
		return err
	}
	if systemDatabases[req.DBName] {
		return fmt.Errorf("database %q is a system database and cannot be deleted", req.DBName)
	}

	host, port, adminUser, adminPass, err := s.resolve(req.JobID)
	if err != nil {
		return err
	}

	db, err := s.connect(ctx, host, port, "", adminUser, adminPass)
	if err != nil {
		return err
	}
	defer db.Close()

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

	if _, err := db.ExecContext(ctx, "DROP DATABASE "+mysqlID(req.DBName)); err != nil {
		return fmt.Errorf("drop database: %w", err)
	}
	return nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

/**
 * connect.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   host string - the host string
 *   port int - the port value
 *   dbname string - the dbname string
 *   user string - the user string
 *   password string - the password string
 *
 * Returns:
 *   *sql.DB - the resulting *sql.DB
 *   error - error value; non-nil when the operation fails
 */
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
	// Secure by default: require TLS with full verification on admin connections.
	// Opt out for self-signed/IP-SAN clusters via CLUSTER_DB_TLS_MODE
	// (true|skip-verify|false).
	cfg.TLSConfig = adminTLSMode()

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

/**
 * adminTLSMode resolves the go-sql-driver TLS mode for admin connections.
 * Defaults to "true" (TLS with full verification); operators may relax it via
 * CLUSTER_DB_TLS_MODE for clusters using self-signed or IP-SAN certificates.
 *
 * Returns:
 *   string - the resulting string
 */
func adminTLSMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CLUSTER_DB_TLS_MODE"))) {
	case "skip-verify":
		return "skip-verify"
	case "false", "disable", "off":
		return "false"
	default:
		return "true"
	}
}

/**
 * mysqlID.
 *
 * Params:
 *   name string - the name string
 *
 * Returns:
 *   string - the resulting string
 */
func mysqlID(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

/**
 * mysqlLit.
 *
 * Params:
 *   s string - the s string
 *
 * Returns:
 *   string - the resulting string
 */
func mysqlLit(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return "'" + s + "'"
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
