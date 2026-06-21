package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// defaultStepTimeout bounds a single Ansible invocation when the caller did not
// supply one.
const defaultStepTimeout = 15 * time.Minute

// AnsibleSpec fully describes one ansible-playbook invocation. The engine
// supplies the engine-specific bits (playbook, inventory YAML, extra-vars, tags)
// and AnsibleRun owns the mechanical execution: temp workspace, file writes,
// argv assembly, process exec, output capture, and result mapping.
type AnsibleSpec struct {
	Bin             string         // ansible-playbook binary
	Playbook        string         // playbook path
	Inventory       string         // inventory YAML content
	ExtraVars       map[string]any // written to vars.json, passed as --extra-vars @file
	Tags            []string       // optional --tags values (joined with ",")
	Verbosity       int            // 0 = quiet; n>0 appends -vvv (n v's)
	StreamLogs      bool           // also tee stdout/stderr to the process streams
	MaxOutputChars  int            // cap on captured stdout/stderr (0 = unlimited)
	Timeout         time.Duration  // per-invocation timeout; <=0 uses defaultStepTimeout
	StepName        string         // recorded as StepResult.Name
	WorkspacePrefix string         // os.MkdirTemp prefix
	Env             []string       // extra environment appended to os.Environ()
}

/**
 * AnsibleRun executes one ansible-playbook invocation and maps its outcome to a
 * StepResult. Secrets travel via an on-disk vars.json (0600) passed with
 * `--extra-vars @file` — never on the command line or through a shell — so they
 * are not exposed in the process table and there is no shell-injection surface.
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   spec AnsibleSpec - the spec (AnsibleSpec)
 *
 * Returns:
 *   result StepResult - the result (StepResult)
 */
func AnsibleRun(ctx context.Context, spec AnsibleSpec) (result StepResult) {
	result = StepResult{
		Name:      spec.StepName,
		Status:    JobStatusRunning,
		StartedAt: time.Now().UTC(),
		ExitCode:  -1,
	}
	defer func() { result.EndedAt = time.Now().UTC() }()

	if strings.TrimSpace(spec.Playbook) == "" {
		result.Status = JobStatusFailed
		result.Message = "playbook path is not configured"
		return
	}

	workspace, err := os.MkdirTemp("", spec.WorkspacePrefix)
	if err != nil {
		result.Status = JobStatusFailed
		result.Message = fmt.Sprintf("create temp dir: %v", err)
		return
	}
	defer os.RemoveAll(workspace)

	inventoryPath := filepath.Join(workspace, "inventory.yml")
	varsPath := filepath.Join(workspace, "vars.json")

	if err := os.WriteFile(inventoryPath, []byte(spec.Inventory), 0o600); err != nil {
		result.Status = JobStatusFailed
		result.Message = fmt.Sprintf("write inventory: %v", err)
		return
	}

	sanitized, err := json.Marshal(spec.ExtraVars)
	if err != nil {
		result.Status = JobStatusFailed
		result.Message = fmt.Sprintf("marshal vars: %v", err)
		return
	}
	if err := os.WriteFile(varsPath, sanitized, 0o600); err != nil {
		result.Status = JobStatusFailed
		result.Message = fmt.Sprintf("write vars: %v", err)
		return
	}

	runTimeout := spec.Timeout
	if runTimeout <= 0 {
		runTimeout = defaultStepTimeout
	}
	stepCtx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()

	args := []string{"-i", inventoryPath, spec.Playbook}
	if len(spec.Tags) > 0 {
		args = append(args, "--tags", strings.Join(spec.Tags, ","))
	}
	args = append(args, "--extra-vars", "@"+varsPath)
	if spec.Verbosity > 0 {
		args = append(args, "-"+strings.Repeat("v", spec.Verbosity))
	}

	cmd := exec.CommandContext(stepCtx, spec.Bin, args...)
	cmd.Env = append(os.Environ(), spec.Env...)

	var stdout, stderr cappedBuffer
	stdout.limit = spec.MaxOutputChars
	stderr.limit = spec.MaxOutputChars
	if spec.StreamLogs {
		cmd.Stdout = io.MultiWriter(&stdout, os.Stdout)
		cmd.Stderr = io.MultiWriter(&stderr, os.Stderr)
	} else {
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	}

	err = cmd.Run()
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()

	if err == nil {
		result.Status = JobStatusCompleted
		result.ExitCode = 0
		return
	}

	result.Status = JobStatusFailed
	if exitErr, ok := err.(*exec.ExitError); ok {
		result.ExitCode = exitErr.ExitCode()
	} else {
		result.ExitCode = 1
	}
	if stepCtx.Err() == context.DeadlineExceeded {
		result.Message = "step execution timed out"
		return
	}
	result.Message = fmt.Sprintf("ansible step failed: %v", err)
	return
}

// cappedBuffer is a write-capped bytes.Buffer. Writes beyond limit are dropped
// and a truncation marker is appended to String(). limit=0 means unlimited.
type cappedBuffer struct {
	buf     bytes.Buffer
	limit   int
	dropped bool
}

/**
 * Write.
 *
 * Receiver:
 *   b *cappedBuffer - pointer receiver; the method may mutate this cappedBuffer instance
 *
 * Params:
 *   p []byte - the p bytes
 *
 * Returns:
 *   int - the resulting integer
 *   error - error value; non-nil when the operation fails
 */
func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.limit > 0 {
		avail := b.limit - b.buf.Len()
		if avail <= 0 {
			b.dropped = true
			return len(p), nil
		}
		if len(p) > avail {
			_, _ = b.buf.Write(p[:avail])
			b.dropped = true
			return len(p), nil
		}
	}
	return b.buf.Write(p)
}

/**
 * String.
 *
 * Receiver:
 *   b *cappedBuffer - pointer receiver; the method may mutate this cappedBuffer instance
 *
 * Returns:
 *   string - the resulting string
 */
func (b *cappedBuffer) String() string {
	s := strings.TrimSpace(b.buf.String())
	if b.dropped {
		s += "\n...truncated..."
	}
	return s
}
