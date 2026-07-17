package dbmanager

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	userPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_-]{1,31}$`)
	dbPattern   = regexp.MustCompile(`^[a-zA-Z0-9_]{1,64}$`)
)

/**
 * validate.
 *
 * Receiver:
 *   req *CreateUserRequest - pointer receiver; the method may mutate this CreateUserRequest instance
 *
 * Returns:
 *   error - error value; non-nil when the operation fails
 */
func (req *CreateUserRequest) validate() error {
	req.Username = strings.TrimSpace(req.Username)
	req.DatabaseName = strings.TrimSpace(req.DatabaseName)
	if strings.TrimSpace(req.JobID) == "" {
		return fmt.Errorf("job_id is required")
	}
	if !userPattern.MatchString(req.Username) {
		return fmt.Errorf("username must match %s", userPattern)
	}
	if strings.TrimSpace(req.Password) == "" {
		return fmt.Errorf("password is required")
	}
	if req.DatabaseName != "" && !dbPattern.MatchString(req.DatabaseName) {
		return fmt.Errorf("database must match %s", dbPattern)
	}
	return nil
}

/**
 * validate.
 *
 * Receiver:
 *   req *ResetPasswordRequest - pointer receiver; the method may mutate this ResetPasswordRequest instance
 *
 * Returns:
 *   error - error value; non-nil when the operation fails
 */
func (req *ResetPasswordRequest) validate() error {
	req.Username = strings.TrimSpace(req.Username)
	if strings.TrimSpace(req.JobID) == "" {
		return fmt.Errorf("job_id is required")
	}
	if !userPattern.MatchString(req.Username) {
		return fmt.Errorf("username must match %s", userPattern)
	}
	if strings.TrimSpace(req.Password) == "" {
		return fmt.Errorf("password is required")
	}
	return nil
}

/**
 * validate.
 *
 * Receiver:
 *   req *UpdateUserRequest - pointer receiver; the method may mutate this UpdateUserRequest instance
 *
 * Returns:
 *   error - error value; non-nil when the operation fails
 */
func (req *UpdateUserRequest) validate() error {
	req.Username = strings.TrimSpace(req.Username)
	req.NewUsername = strings.TrimSpace(req.NewUsername)
	if strings.TrimSpace(req.JobID) == "" {
		return fmt.Errorf("job_id is required")
	}
	if !userPattern.MatchString(req.Username) {
		return fmt.Errorf("username must match %s", userPattern)
	}
	if !userPattern.MatchString(req.NewUsername) {
		return fmt.Errorf("new_username must match %s", userPattern)
	}
	return nil
}

/**
 * validate.
 *
 * Receiver:
 *   req *DeleteUserRequest - pointer receiver; the method may mutate this DeleteUserRequest instance
 *
 * Returns:
 *   error - error value; non-nil when the operation fails
 */
func (req *DeleteUserRequest) validate() error {
	req.Username = strings.TrimSpace(req.Username)
	if strings.TrimSpace(req.JobID) == "" {
		return fmt.Errorf("job_id is required")
	}
	if !userPattern.MatchString(req.Username) {
		return fmt.Errorf("username must match %s", userPattern)
	}
	return nil
}

/**
 * validate.
 *
 * Receiver:
 *   req *CreateDatabaseRequest - pointer receiver; the method may mutate this CreateDatabaseRequest instance
 *
 * Returns:
 *   error - error value; non-nil when the operation fails
 */
func (req *CreateDatabaseRequest) validate() error {
	req.DBName = strings.TrimSpace(req.DBName)
	if strings.TrimSpace(req.JobID) == "" {
		return fmt.Errorf("job_id is required")
	}
	if !dbPattern.MatchString(req.DBName) {
		return fmt.Errorf("dbname must match %s", dbPattern)
	}
	return nil
}

/**
 * validate.
 *
 * Receiver:
 *   req *UpdateDatabaseRequest - pointer receiver; the method may mutate this UpdateDatabaseRequest instance
 *
 * Returns:
 *   error - error value; non-nil when the operation fails
 */
func (req *UpdateDatabaseRequest) validate() error {
	req.DBName = strings.TrimSpace(req.DBName)
	req.NewDBName = strings.TrimSpace(req.NewDBName)
	if strings.TrimSpace(req.JobID) == "" {
		return fmt.Errorf("job_id is required")
	}
	if !dbPattern.MatchString(req.DBName) {
		return fmt.Errorf("dbname must match %s", dbPattern)
	}
	if !dbPattern.MatchString(req.NewDBName) {
		return fmt.Errorf("new_dbname must match %s", dbPattern)
	}
	return nil
}

/**
 * validate.
 *
 * Receiver:
 *   req *DeleteDatabaseRequest - pointer receiver; the method may mutate this DeleteDatabaseRequest instance
 *
 * Returns:
 *   error - error value; non-nil when the operation fails
 */
func (req *DeleteDatabaseRequest) validate() error {
	req.DBName = strings.TrimSpace(req.DBName)
	if strings.TrimSpace(req.JobID) == "" {
		return fmt.Errorf("job_id is required")
	}
	if !dbPattern.MatchString(req.DBName) {
		return fmt.Errorf("dbname must match %s", dbPattern)
	}
	return nil
}

/**
 * validate.
 *
 * Receiver:
 *   req *SetConnectionLimitRequest - pointer receiver; the method may mutate this SetConnectionLimitRequest instance
 *
 * Returns:
 *   error - error value; non-nil when the operation fails
 */
func (req *SetConnectionLimitRequest) validate() error {
	req.JobID = strings.TrimSpace(req.JobID)
	if req.JobID == "" {
		return fmt.Errorf("job_id is required")
	}
	// Same bounds as the deploy-time connection_limit, but an edit must be
	// explicit: 0 (engine default) is only meaningful at deploy time.
	if req.ConnectionLimit < 10 || req.ConnectionLimit > 100000 {
		return fmt.Errorf("connection_limit must be between 10 and 100000")
	}
	return nil
}
