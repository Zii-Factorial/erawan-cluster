package mysql

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	namePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{3,64}$`)
	userPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_-]{1,31}$`)
	dbPattern   = regexp.MustCompile(`^[a-zA-Z0-9_]{1,64}$`)
)

func ValidateDeployRequest(req *DeployRequest) error {
	req.AdminUsername = strings.TrimSpace(req.AdminUsername)
	req.ClusterName = strings.TrimSpace(req.ClusterName)
	req.PrimaryIP = strings.TrimSpace(req.PrimaryIP)
	req.NewUser = strings.TrimSpace(req.NewUser)
	req.NewDB = strings.TrimSpace(req.NewDB)
	if req.ClusterName == "" {
		req.ClusterName = "prodCluster"
	}
	if req.AdminUsername == "" {
		req.AdminUsername = "clusteradmin"
	}
	if len(req.StandbyIPs) == 0 && len(req.SecondaryIPs) > 0 {
		req.StandbyIPs = req.SecondaryIPs
	}

	if !userPattern.MatchString(req.AdminUsername) {
		return fmt.Errorf("cluster_admin_username must match %s", userPattern.String())
	}
	if !namePattern.MatchString(req.ClusterName) {
		return fmt.Errorf("cluster_name must match %s", namePattern.String())
	}
	if req.NewUser != "" && !userPattern.MatchString(req.NewUser) {
		return fmt.Errorf("new_user must match %s", userPattern.String())
	}
	if req.NewDB != "" && !dbPattern.MatchString(req.NewDB) {
		return fmt.Errorf("new_db must match %s", dbPattern.String())
	}

	hasInitDBInput := req.NewUser != "" || req.NewDB != "" || strings.TrimSpace(req.NewUserPassword) != ""
	if hasInitDBInput {
		if req.NewUser == "" {
			return fmt.Errorf("new_user is required when init DB fields are provided")
		}
		if strings.TrimSpace(req.NewUserPassword) == "" {
			return fmt.Errorf("new_user_password is required when init DB fields are provided")
		}
		if req.NewDB == "" {
			return fmt.Errorf("new_db is required when init DB fields are provided")
		}
	}

	if net.ParseIP(req.PrimaryIP) == nil {
		return fmt.Errorf("primary_ip must be a valid IP address")
	}
	seen := map[string]struct{}{req.PrimaryIP: {}}
	for i, ip := range req.StandbyIPs {
		ip = strings.TrimSpace(ip)
		if net.ParseIP(ip) == nil {
			return fmt.Errorf("standby_ips[%d] must be a valid IP address", i)
		}
		if _, ok := seen[ip]; ok {
			return fmt.Errorf("duplicate IP detected: %s", ip)
		}
		seen[ip] = struct{}{}
		req.StandbyIPs[i] = ip
	}

	if req.SSHPort == 0 {
		req.SSHPort = 22
	}
	if req.SSHPort < 1 || req.SSHPort > 65535 {
		return fmt.Errorf("ssh_port must be between 1 and 65535")
	}
	if req.MySQLPort == 0 {
		req.MySQLPort = 3306
	}
	if req.MySQLPort < 1 || req.MySQLPort > 65535 {
		return fmt.Errorf("mysql_port must be between 1 and 65535")
	}
	if req.MySQLVersion == 0 {
		req.MySQLVersion = 8
	}
	switch req.MySQLVersion {
	case 7, 8, 9: // supported: 7=5.7, 8=8.x, 9=9.x
	default:
		return fmt.Errorf("mysql_version %d is not supported; valid: 7, 8, 9", req.MySQLVersion)
	}

	if req.StepTimeoutSeconds == 0 {
		req.StepTimeoutSeconds = 900
	}
	if req.StepTimeoutSeconds < 30 || req.StepTimeoutSeconds > 7200 {
		return fmt.Errorf("step_timeout_seconds must be between 30 and 7200")
	}

	return nil
}

func ValidateResumeSecrets(req ResumeRequest) (SecretInput, error) {
	secret := SecretInput{
		RootPassword:         strings.TrimSpace(req.RootPassword),
		AdminPassword: strings.TrimSpace(req.AdminPassword),
		NewUserPassword:      strings.TrimSpace(req.NewUserPassword),
	}
	return secret, nil
}

func ValidateRollbackSecrets(req RollbackRequest) (SecretInput, error) {
	secret := SecretInput{
		RootPassword:         strings.TrimSpace(req.RootPassword),
		AdminPassword: strings.TrimSpace(req.AdminPassword),
	}
	return secret, nil
}

func ValidateAddMemberRequest(req *AddMemberRequest) error {
	req.MemberIP = strings.TrimSpace(req.MemberIP)
	if net.ParseIP(req.MemberIP) == nil {
		return fmt.Errorf("member_ip must be a valid IP address")
	}
	return nil
}

func ValidateRemoveMemberRequest(req *RemoveMemberRequest) error {
	req.MemberIP = strings.TrimSpace(req.MemberIP)
	if net.ParseIP(req.MemberIP) == nil {
		return fmt.Errorf("member_ip must be a valid IP address")
	}
	return nil
}

func ValidateServiceSSHConfig(user, privateKeyPath string) (string, string, error) {
	normalizedUser := strings.TrimSpace(user)
	if !userPattern.MatchString(normalizedUser) {
		return "", "", fmt.Errorf("ssh_user must match %s", userPattern.String())
	}
	normalizedKeyPath, err := normalizeSSHPrivateKeyPath(privateKeyPath)
	if err != nil {
		return "", "", fmt.Errorf("ssh_private_key_path: %w", err)
	}
	if normalizedKeyPath == "" {
		return "", "", fmt.Errorf("ssh_private_key_path is required")
	}
	return normalizedUser, normalizedKeyPath, nil
}

func normalizeSSHPrivateKeyPath(raw string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" {
		return "", nil
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home: %w", err)
		}
		path = filepath.Join(home, path[2:])
	}
	if !filepath.IsAbs(path) {
		absPath, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolve absolute path: %w", err)
		}
		path = absPath
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat %q: %w", path, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%q must be a file", path)
	}
	return path, nil
}
