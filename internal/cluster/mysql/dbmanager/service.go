package dbmanager

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	mysql "erawan-cluster/internal/cluster/mysql"
	gomysql "github.com/go-sql-driver/mysql"
)

type Service struct {
	store *mysql.Store
}

func NewService(store *mysql.Store) *Service { return &Service{store: store} }

// resolve loads primary IP, port, and admin credentials from the stored job.
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

	users, err := appUsers(ctx, db)
	if err != nil {
		return fmt.Errorf("list users: %w", err)
	}
	dbid := mysqlID(req.DBName)
	for _, u := range users {
		if _, err := db.ExecContext(ctx, fmt.Sprintf(
			"GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, ALTER, INDEX, REFERENCES ON %s.* TO %s@'%%'",
			dbid, mysqlID(u)),
		); err != nil {
			return fmt.Errorf("grant on %s to %s: %w", req.DBName, u, err)
		}
	}
	if _, err := db.ExecContext(ctx, "FLUSH PRIVILEGES"); err != nil {
		return fmt.Errorf("flush privileges: %w", err)
	}
	return nil
}

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

func mysqlID(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

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
