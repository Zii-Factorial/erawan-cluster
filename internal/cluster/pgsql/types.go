package pgsql

import (
	"encoding/json"

	"erawan-cluster/internal/cluster/core"
)

// Job status values, re-exported from core so existing references keep working.
const (
	JobStatusPending   = core.JobStatusPending
	JobStatusRunning   = core.JobStatusRunning
	JobStatusFailed    = core.JobStatusFailed
	JobStatusCompleted = core.JobStatusCompleted
)

// Shared job state types are provided by core; these aliases keep the engine's
// public API (pgsql.Job, pgsql.StepResult, ...) unchanged.
type (
	Job             = core.Job[StoredSpec]
	StepResult      = core.StepResult
	MemberOperation = core.MemberOperation
)

type DeployRequest struct {
	ClusterName        string   `json:"cluster_name"`
	PrimaryIP          string   `json:"primary_ip"`
	StandbyIPs         []string `json:"standby_ips"`
	PostgresPassword   string   `json:"postgres_password"`
	ReplicatorPassword string   `json:"replicator_password"`
	AdminUsername      string   `json:"admin_username"`
	AdminPassword      string   `json:"admin_password"`
	NewUser            string   `json:"new_user"`
	NewUserPassword    string   `json:"new_user_password"`
	NewUserSSLRequired *bool    `json:"new_user_ssl_required"`
	NewUserSuperuser   *bool    `json:"new_user_superuser"`
	NewDB              string   `json:"new_db"`
	SSHPort            int      `json:"ssh_port"`
	PostgresPort       int      `json:"postgres_port"`
	PostgresVersion    int      `json:"postgres_version"` // major version; default 16
	StepTimeoutSeconds int      `json:"step_timeout_seconds"`
}

type ResumeRequest struct {
	PostgresPassword   string `json:"postgres_password"`
	ReplicatorPassword string `json:"replicator_password"`
	AdminPassword      string `json:"admin_password"`
	NewUserPassword    string `json:"new_user_password"`
}

type StoredSpec struct {
	ClusterName        string   `json:"cluster_name"`
	PrimaryIP          string   `json:"primary_ip"`
	StandbyIPs         []string `json:"standby_ips"`
	AdminUsername      string   `json:"admin_username"`
	NewUser            string   `json:"new_user"`
	NewUserSSLRequired bool     `json:"new_user_ssl_required"`
	NewUserSuperuser   bool     `json:"new_user_superuser"`
	NewDB              string   `json:"new_db"`
	SSHUser            string   `json:"ssh_user"`
	SSHPrivateKeyPath  string   `json:"ssh_private_key_path,omitempty"`
	SSHPort            int      `json:"ssh_port"`
	PostgresPort       int      `json:"postgres_port"`
	PostgresVersion    int      `json:"postgres_version"`
	StepTimeoutSeconds int      `json:"step_timeout_seconds"`
}

type SecretInput struct {
	PostgresPassword   string
	ReplicatorPassword string
	AdminPassword      string
	NewUserPassword    string
	ExporterPassword   string
}

type StoredSecret struct {
	PostgresUser       string `json:"postgres_user"`
	PostgresPassword   string `json:"postgres_password"`
	ReplicatorUser     string `json:"replicator_user"`
	ReplicatorPassword string `json:"replicator_password"`
	AdminPassword      string `json:"admin_password"`
	ExporterPassword   string `json:"exporter_password,omitempty"`
}

type AddMemberRequest struct {
	JobID     string   `json:"job_id"`
	MemberIPs []string `json:"member_ips"`
}

type RemoveMemberRequest struct {
	JobID    string `json:"job_id"`
	MemberIP string `json:"member_ip"`
	Force    bool   `json:"force,omitempty"`
}

/**
 * NewUserSSLRequiredEnabled.
 *
 * Receiver:
 *   r DeployRequest - value receiver; the method operates on a copy of the DeployRequest
 *
 * Returns:
 *   bool - boolean result
 */
func (r DeployRequest) NewUserSSLRequiredEnabled() bool {
	if r.NewUserSSLRequired == nil {
		return true
	}
	return *r.NewUserSSLRequired
}

func (r DeployRequest) NewUserSuperuserEnabled() bool {
	if r.NewUserSuperuser == nil {
		return false
	}
	return *r.NewUserSuperuser
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
		NewUserSSLRequired *bool `json:"new_user_ssl_required"`
		NewUserSuperuser   *bool `json:"new_user_superuser"`
	}{
		alias: (*alias)(s),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if aux.NewUserSSLRequired == nil {
		s.NewUserSSLRequired = true
	} else {
		s.NewUserSSLRequired = *aux.NewUserSSLRequired
	}
	if aux.NewUserSuperuser == nil {
		s.NewUserSuperuser = false
	} else {
		s.NewUserSuperuser = *aux.NewUserSuperuser
	}
	return nil
}
