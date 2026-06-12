package dbmanager

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

var (
	userPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_-]{1,31}$`)
	dbPattern   = regexp.MustCompile(`^[a-zA-Z0-9_]{1,64}$`)
)

func (req *CreateUserRequest) validate() error {
	req.PrimaryIP = strings.TrimSpace(req.PrimaryIP)
	req.AdminUser = strings.TrimSpace(req.AdminUser)
	req.Username = strings.TrimSpace(req.Username)
	if net.ParseIP(req.PrimaryIP) == nil {
		return fmt.Errorf("primary_ip must be a valid IP address")
	}
	if req.Port == 0 {
		req.Port = 3306
	}
	if req.Port < 1 || req.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if req.AdminUser == "" {
		return fmt.Errorf("admin_user is required")
	}
	if req.AdminPassword == "" {
		return fmt.Errorf("admin_password is required")
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
	req.PrimaryIP = strings.TrimSpace(req.PrimaryIP)
	req.AdminUser = strings.TrimSpace(req.AdminUser)
	req.Username = strings.TrimSpace(req.Username)
	if net.ParseIP(req.PrimaryIP) == nil {
		return fmt.Errorf("primary_ip must be a valid IP address")
	}
	if req.Port == 0 {
		req.Port = 3306
	}
	if req.Port < 1 || req.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if req.AdminUser == "" {
		return fmt.Errorf("admin_user is required")
	}
	if req.AdminPassword == "" {
		return fmt.Errorf("admin_password is required")
	}
	if !userPattern.MatchString(req.Username) {
		return fmt.Errorf("username must match %s", userPattern)
	}
	return nil
}

func (req *CreateDatabaseRequest) validate() error {
	req.PrimaryIP = strings.TrimSpace(req.PrimaryIP)
	req.AdminUser = strings.TrimSpace(req.AdminUser)
	req.DBName = strings.TrimSpace(req.DBName)
	if net.ParseIP(req.PrimaryIP) == nil {
		return fmt.Errorf("primary_ip must be a valid IP address")
	}
	if req.Port == 0 {
		req.Port = 3306
	}
	if req.Port < 1 || req.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if req.AdminUser == "" {
		return fmt.Errorf("admin_user is required")
	}
	if req.AdminPassword == "" {
		return fmt.Errorf("admin_password is required")
	}
	if !dbPattern.MatchString(req.DBName) {
		return fmt.Errorf("dbname must match %s", dbPattern)
	}
	return nil
}

func (req *DeleteDatabaseRequest) validate() error {
	req.PrimaryIP = strings.TrimSpace(req.PrimaryIP)
	req.AdminUser = strings.TrimSpace(req.AdminUser)
	req.DBName = strings.TrimSpace(req.DBName)
	if net.ParseIP(req.PrimaryIP) == nil {
		return fmt.Errorf("primary_ip must be a valid IP address")
	}
	if req.Port == 0 {
		req.Port = 3306
	}
	if req.Port < 1 || req.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if req.AdminUser == "" {
		return fmt.Errorf("admin_user is required")
	}
	if req.AdminPassword == "" {
		return fmt.Errorf("admin_password is required")
	}
	if !dbPattern.MatchString(req.DBName) {
		return fmt.Errorf("dbname must match %s", dbPattern)
	}
	return nil
}
