package pgsql

import (
	"encoding/json"
	"time"
)

const (
	JobStatusPending   = "pending"
	JobStatusRunning   = "running"
	JobStatusFailed    = "failed"
	JobStatusCompleted = "completed"
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
	NewDB              string   `json:"new_db"`
	SSHPort            int      `json:"ssh_port"`
	PostgresPort       int      `json:"postgres_port"`
	PostgresVersion    int      `json:"postgres_version"`    // major version; default 16
	StepTimeoutSeconds int      `json:"step_timeout_seconds"`
}

type ResumeRequest struct {
	PostgresPassword   string `json:"postgres_password"`
	ReplicatorPassword string `json:"replicator_password"`
	AdminPassword      string `json:"admin_password"`
	NewUserPassword    string `json:"new_user_password"`
}

type StepResult struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	ExitCode  int       `json:"exit_code"`
	Stdout    string    `json:"stdout,omitempty"`
	Stderr    string    `json:"stderr,omitempty"`
	Message   string    `json:"message,omitempty"`
}

type Job struct {
	ID                string       `json:"id"`
	Status            string       `json:"status"`
	CreatedAt         time.Time    `json:"created_at"`
	UpdatedAt         time.Time    `json:"updated_at"`
	CurrentStep       string       `json:"current_step,omitempty"`
	LastCompletedStep int          `json:"last_completed_step"`
	CompletedSteps    int          `json:"completed_steps"`
	TotalSteps        int          `json:"total_steps"`
	ProgressPercent   int          `json:"progress_percent"`
	Error             string       `json:"error,omitempty"`
	Request           StoredSpec   `json:"request"`
	Steps             []StepResult `json:"steps"`
}

type StoredSpec struct {
	ClusterName        string   `json:"cluster_name"`
	PrimaryIP          string   `json:"primary_ip"`
	StandbyIPs         []string `json:"standby_ips"`
	AdminUsername      string   `json:"admin_username"`
	NewUser            string   `json:"new_user"`
	NewUserSSLRequired bool     `json:"new_user_ssl_required"`
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
}

type StoredSecret struct {
	PostgresUser       string `json:"postgres_user"`
	PostgresPassword   string `json:"postgres_password"`
	ReplicatorUser     string `json:"replicator_user"`
	ReplicatorPassword string `json:"replicator_password"`
	AdminPassword      string `json:"admin_password"`
}

func (r DeployRequest) NewUserSSLRequiredEnabled() bool {
	if r.NewUserSSLRequired == nil {
		return true
	}
	return *r.NewUserSSLRequired
}

func (s *StoredSpec) UnmarshalJSON(data []byte) error {
	type alias StoredSpec
	aux := struct {
		*alias
		NewUserSSLRequired *bool `json:"new_user_ssl_required"`
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
	return nil
}
