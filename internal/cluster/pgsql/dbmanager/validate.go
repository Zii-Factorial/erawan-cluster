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
