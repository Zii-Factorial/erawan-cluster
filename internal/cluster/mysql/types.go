package mysql

import (
	"encoding/json"
	"time"
)

const (
	JobStatusPending    = "pending"
	JobStatusRunning    = "running"
	JobStatusFailed     = "failed"
	JobStatusCompleted  = "completed"
	JobStatusRolledBack = "rolled_back"
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
	NewUserSSLRequired bool     `json:"new_user_ssl_required"`
	NewDB              string   `json:"new_db"`
	AssumePrepared     bool     `json:"assume_prepared"`
	BootstrapRouter    *bool    `json:"bootstrap_router"`
	SSHPort            int      `json:"ssh_port"`
	MySQLPort          int      `json:"mysql_port"`
	MySQLVersion       int      `json:"mysql_version"`       // major version: 7=5.7, 8=8.x, 9=9.x; default 8
	StepTimeoutSeconds int      `json:"step_timeout_seconds"`
}

func (r DeployRequest) BootstrapRouterEnabled() bool {
	if r.BootstrapRouter == nil {
		return true
	}
	return *r.BootstrapRouter
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
	AdminUsername      string   `json:"admin_username"`
	ClusterName        string   `json:"cluster_name"`
	PrimaryIP          string   `json:"primary_ip"`
	StandbyIPs         []string `json:"standby_ips"`
	NewUser            string   `json:"new_user"`
	NewUserSSLRequired bool     `json:"new_user_ssl_required"`
	NewDB              string   `json:"new_db"`
	AssumePrepared     bool     `json:"assume_prepared"`
	BootstrapRouter    bool     `json:"bootstrap_router"`
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
	MemberIP       string `json:"member_ip"`
	AdminPassword  string `json:"admin_password,omitempty"`
	AssumePrepared bool   `json:"assume_prepared"`
}

type RemoveMemberRequest struct {
	MemberIP      string `json:"member_ip"`
	AdminPassword string `json:"admin_password,omitempty"`
	Force         bool   `json:"force,omitempty"`
}

type MemberOperationResult struct {
	Action   string     `json:"action"`
	MemberIP string     `json:"member_ip"`
	Spec     StoredSpec `json:"spec"`
	Step     StepResult `json:"step"`
}
