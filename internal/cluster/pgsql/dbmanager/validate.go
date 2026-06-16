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
