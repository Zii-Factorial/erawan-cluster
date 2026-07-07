package mysql

import (
	"encoding/json"

	"erawan-cluster/internal/cluster/core"
)

// Job status values, re-exported from core so existing references keep working.
const (
	JobStatusPending    = core.JobStatusPending
	JobStatusRunning    = core.JobStatusRunning
	JobStatusFailed     = core.JobStatusFailed
	JobStatusCompleted  = core.JobStatusCompleted
	JobStatusRolledBack = core.JobStatusRolledBack
)

// Shared job state types are provided by core; these aliases keep the engine's
// public API (mysql.Job, mysql.StepResult, ...) unchanged.
type (
	Job             = core.Job[StoredSpec]
	StepResult      = core.StepResult
	MemberOperation = core.MemberOperation
)

type DeployRequest struct {
	RootPassword       string   `json:"root_password"`
	AdminUsername      string   `json:"admin_username"`
	AdminPassword      string   `json:"admin_password"`
	ClusterName        string   `json:"cluster_name"`
	PrimaryIP          string   `json:"primary_ip"`
	StandbyIPs         []string `json:"standby_ips"`
	SecondaryIPs       []string `json:"secondary_ips,omitempty"`
	NewUser            string   `json:"new_user"`
	NewUserPassword    string   `json:"new_user_password"`
	NewUserSSLRequired *bool    `json:"new_user_ssl_required"`
	NewUserSuperuser   bool     `json:"new_user_superuser"`
	NewDB              string   `json:"new_db"`
	AssumePrepared     bool     `json:"assume_prepared"`
	SSHPort            int      `json:"ssh_port"`
	MySQLPort          int      `json:"mysql_port"`
	MySQLVersion       int      `json:"mysql_version"` // major version: 7=5.7, 8=8.x, 9=9.x; default 8
	StepTimeoutSeconds int      `json:"step_timeout_seconds"`
}

func (r DeployRequest) NewUserSSLRequiredEnabled() bool {
	if r.NewUserSSLRequired == nil {
		return true
	}
	return *r.NewUserSSLRequired
}

type ResumeRequest struct {
	RootPassword    string `json:"root_password"`
	AdminPassword   string `json:"admin_password"`
	NewUserPassword string `json:"new_user_password"`
}

type RollbackRequest struct {
	RootPassword  string `json:"root_password"`
	AdminPassword string `json:"admin_password"`
}

type StoredSpec struct {
	AdminUsername      string   `json:"admin_username"`
	ClusterName        string   `json:"cluster_name"`
	PrimaryIP          string   `json:"primary_ip"`
	StandbyIPs         []string `json:"standby_ips"`
	NewUser            string   `json:"new_user"`
	NewUserSSLRequired bool     `json:"new_user_ssl_required"`
	NewUserSuperuser   bool     `json:"new_user_superuser"`
	NewDB              string   `json:"new_db"`
	AssumePrepared     bool     `json:"assume_prepared"`
	SSHUser            string   `json:"ssh_user"`
	SSHPrivateKeyPath  string   `json:"ssh_private_key_path,omitempty"`
	SSHPort            int      `json:"ssh_port"`
	MySQLPort          int      `json:"mysql_port"`
	MySQLVersion       int      `json:"mysql_version"`
	StepTimeoutSeconds int      `json:"step_timeout_seconds"`
}

type SecretInput struct {
	RootPassword    string
	AdminPassword   string
	NewUserPassword string
}

type StoredSecret struct {
	AdminUser     string `json:"admin_user"`
	AdminPassword string `json:"admin_password"`
}

/**
 * UnmarshalJSON.
 *
 * Receiver:
 *   s *StoredSpec - pointer receiver; the method may mutate this StoredSpec instance
 *
 * Params:
 *   data []byte - the data bytes
 *
 * Returns:
 *   error - error value; non-nil when the operation fails
 */
func (s *StoredSpec) UnmarshalJSON(data []byte) error {
	type alias StoredSpec
	aux := struct {
		*alias
		SecondaryIPs         []string `json:"secondary_ips"`
		ClusterAdminUsername string   `json:"cluster_admin_username"` // backward compat
	}{
		alias: (*alias)(s),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if len(s.StandbyIPs) == 0 && len(aux.SecondaryIPs) > 0 {
		s.StandbyIPs = aux.SecondaryIPs
	}
	if s.AdminUsername == "" && aux.ClusterAdminUsername != "" {
		s.AdminUsername = aux.ClusterAdminUsername
	}
	return nil
}

/**
 * UnmarshalJSON.
 *
 * Receiver:
 *   s *StoredSecret - pointer receiver; the method may mutate this StoredSecret instance
 *
 * Params:
 *   data []byte - the data bytes
 *
 * Returns:
 *   error - error value; non-nil when the operation fails
 */
func (s *StoredSecret) UnmarshalJSON(data []byte) error {
	type alias StoredSecret
	aux := struct {
		*alias
		ClusterAdminPassword string `json:"cluster_admin_password"` // backward compat
	}{
		alias: (*alias)(s),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if s.AdminPassword == "" && aux.ClusterAdminPassword != "" {
		s.AdminPassword = aux.ClusterAdminPassword
	}
	return nil
}

type AddMemberRequest struct {
	JobID          string   `json:"job_id"`
	MemberIPs      []string `json:"member_ips"`
	AssumePrepared bool     `json:"assume_prepared,omitempty"`
}

type RemoveMemberRequest struct {
	JobID    string `json:"job_id"`
	MemberIP string `json:"member_ip"`
	Force    bool   `json:"force,omitempty"`
}
