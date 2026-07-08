// Package core holds the engine-agnostic machinery shared by every database
// cluster engine (MySQL, PostgreSQL, ...). Engine packages compose these
// generic building blocks — job state, the file-backed store, the Ansible
// execution harness, and progress accounting — and only implement the parts
// that genuinely differ (deploy steps, inventory layout, extra-vars, metrics).
//
// The job state type is generic over the engine's stored spec so persistence
// stays type-safe; engine packages expose thin aliases such as
// `type Job = core.Job[StoredSpec]`, keeping their public API unchanged.
package core

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Job status values shared by every cluster engine. Engines re-export the
// subset they use as package-level constants aliased to these.
const (
	JobStatusPending    = "pending"
	JobStatusRunning    = "running"
	JobStatusFailed     = "failed"
	JobStatusCompleted  = "completed"
	JobStatusRolledBack = "rolled_back"
	// JobStatusSkipped marks a pipeline step that was not applicable for a spec.
	JobStatusSkipped = "skipped"
)

// Step is one stage of a deploy pipeline. Tag maps to an Ansible `--tags` value;
// Skippable steps are conditionally skipped by the engine's skip predicate.
type Step struct {
	Name      string
	Tag       string
	Skippable bool
}

// StepResult is the outcome of a single Ansible step (or a skipped/synthetic
// step). It is engine-independent.
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

// MemberOperation records an add/remove-member action carried by a member job.
type MemberOperation struct {
	Type        string   `json:"type"` // "add" or "remove"
	MemberIPs   []string `json:"member_ips"`
	SourceJobID string   `json:"source_job_id"`
}

// RecoveryOperation records a post-outage cluster-recovery action. It is set on
// recovery jobs created by the Recover service method, linking them back to the
// original deploy job whose cluster configuration they use.
type RecoveryOperation struct {
	SourceJobID string `json:"source_job_id"`
}

// ServiceOperation records a whole-cluster service action (currently "stop"),
// linking the job back to the deploy job whose cluster it acts on. Starting a
// stopped cluster is the recovery operation above — a stopped cluster and a
// cluster after a full outage are the same state to the engines.
type ServiceOperation struct {
	Type        string `json:"type"` // "stop"
	SourceJobID string `json:"source_job_id"`
}

// Job is the persisted state of one cluster operation, generic over the
// engine-specific stored spec carried in Request.
type Job[Spec any] struct {
	ID                string              `json:"id"`
	Status            string              `json:"status"`
	CreatedAt         time.Time           `json:"created_at"`
	UpdatedAt         time.Time           `json:"updated_at"`
	CurrentStep       string              `json:"current_step,omitempty"`
	LastCompletedStep int                 `json:"last_completed_step"`
	CompletedSteps    int                 `json:"completed_steps"`
	TotalSteps        int                 `json:"total_steps"`
	ProgressPercent   int                 `json:"progress_percent"`
	Error             string              `json:"error,omitempty"`
	Request           Spec                `json:"request"`
	Steps             []StepResult        `json:"steps"`
	MemberOp          *MemberOperation    `json:"member_op,omitempty"`
	RecoveryOp        *RecoveryOperation  `json:"recovery_op,omitempty"`
	ServiceOp         *ServiceOperation   `json:"service_op,omitempty"`

	// ActiveMemberJobID holds the ID of an in-flight add/remove-member job
	// for this deploy job, if any. It exists because concurrent member
	// operations against the same cluster race on etcd/Group Replication
	// membership changes (e.g. two overlapping etcd learner promotions),
	// which can transiently break quorum. Set when a member job claims the
	// cluster and cleared when that job finishes, so a second member
	// operation started while one is still running is rejected up front
	// instead of racing at the Ansible layer.
	ActiveMemberJobID string `json:"active_member_job_id,omitempty"`
}

/**
 * NewJobID returns a random 24-hex-character job identifier.
 *
 * Returns:
 *   string - the resulting string
 */
func NewJobID() string {
	raw := make([]byte, 12)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}

/**
 * OrRandomSecret returns value when non-empty, otherwise a fresh random
 * 48-hex-character secret. Used to default unset passwords.
 *
 * Params:
 *   value string - the value string
 *
 * Returns:
 *   string - the resulting string
 */
func OrRandomSecret(value string) string {
	if value != "" {
		return value
	}
	raw := make([]byte, 24)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}
