package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// jobIDPattern matches the 24-character lowercase hex strings produced by NewJobID.
// Enforced on every Store read/write to prevent path traversal via caller-supplied IDs.
var jobIDPattern = regexp.MustCompile(`^[0-9a-f]{24}$`)

func validateJobID(jobID string) error {
	if !jobIDPattern.MatchString(jobID) {
		return fmt.Errorf("invalid job ID %q", jobID)
	}
	return nil
}

// Store is a file-backed job/secret store shared by every engine. It is generic
// over the engine's stored spec (Spec) and stored secret (Sec) types. Jobs are
// written as `<id>.json` and secrets as `<id>.secret.json` under dir, both with
// 0600 permissions. All operations are serialized by a single mutex.
type Store[Spec any, Sec any] struct {
	dir string
	mu  sync.Mutex
}

// JobStore is the persistence contract used by engine services. File-backed
// and database-backed stores both implement it.
type JobStore[Spec any, Sec any] interface {
	Save(job *Job[Spec]) error
	Update(jobID string, mutate func(*Job[Spec]) error) error
	SaveSecret(jobID string, secret Sec) error
	Load(jobID string) (*Job[Spec], error)
	LoadSecret(jobID string) (Sec, error)
	List(limit int) ([]Job[Spec], error)
	MarkStaleRunningJobsFailed()
}

/**
 * NewStore creates the state directory (0700) if needed and returns a Store.
 *
 * Params:
 *   dir string - the dir string
 *
 * Returns:
 *   *Store[Spec, Sec] - the resulting *Store[Spec, Sec]
 *   error - error value; non-nil when the operation fails
 */
func NewStore[Spec any, Sec any](dir string) (*Store[Spec, Sec], error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("state directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	return &Store[Spec, Sec]{dir: dir}, nil
}

/**
 * Save persists job, stamping UpdatedAt.
 *
 * Receiver:
 *   s *Store[Spec, Sec] - pointer receiver; the method may mutate this Store[Spec, Sec] instance
 *
 * Params:
 *   job *Job[Spec] - the job (*Job[Spec])
 *
 * Returns:
 *   error - error value; non-nil when the operation fails
 */
func (s *Store[Spec, Sec]) Save(job *Job[Spec]) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(job)
}

/**
 * saveLocked persists job; the caller must hold s.mu.
 *
 * Receiver:
 *   s *Store[Spec, Sec] - pointer receiver; the method may mutate this Store[Spec, Sec] instance
 *
 * Params:
 *   job *Job[Spec] - the job (*Job[Spec])
 *
 * Returns:
 *   error - error value; non-nil when the operation fails
 */
func (s *Store[Spec, Sec]) saveLocked(job *Job[Spec]) error {
	job.UpdatedAt = time.Now().UTC()
	payload, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal job: %w", err)
	}
	return os.WriteFile(s.path(job.ID), payload, 0o600)
}

/**
 * Update atomically loads, mutates, and re-saves a job while holding the store
 * lock for the whole read-modify-write, so concurrent updates cannot clobber
 * one another (e.g. two member operations editing the same deploy job's
 * standby list).
 *
 * Receiver:
 *   s *Store[Spec, Sec] - pointer receiver; the method may mutate this Store[Spec, Sec] instance
 *
 * Params:
 *   jobID string - the jobID string
 *   mutate func(*Job[Spec]) error - the mutate (func(*Job[Spec]) error)
 *
 * Returns:
 *   error - error value; non-nil when the operation fails
 */
func (s *Store[Spec, Sec]) Update(jobID string, mutate func(*Job[Spec]) error) error {
	if err := validateJobID(jobID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, err := s.loadLocked(jobID)
	if err != nil {
		return err
	}
	if err := mutate(job); err != nil {
		return err
	}
	return s.saveLocked(job)
}

/**
 * SaveSecret persists the secret sidecar for jobID.
 *
 * Receiver:
 *   s *Store[Spec, Sec] - pointer receiver; the method may mutate this Store[Spec, Sec] instance
 *
 * Params:
 *   jobID string - the jobID string
 *   secret Sec - the secret (Sec)
 *
 * Returns:
 *   error - error value; non-nil when the operation fails
 */
func (s *Store[Spec, Sec]) SaveSecret(jobID string, secret Sec) error {
	if err := validateJobID(jobID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	payload, err := json.Marshal(secret)
	if err != nil {
		return fmt.Errorf("marshal job secret: %w", err)
	}
	return os.WriteFile(s.secretPath(jobID), payload, 0o600)
}

/**
 * Load reads and decodes the job for jobID.
 *
 * Receiver:
 *   s *Store[Spec, Sec] - pointer receiver; the method may mutate this Store[Spec, Sec] instance
 *
 * Params:
 *   jobID string - the jobID string
 *
 * Returns:
 *   *Job[Spec] - the resulting *Job[Spec]
 *   error - error value; non-nil when the operation fails
 */
func (s *Store[Spec, Sec]) Load(jobID string) (*Job[Spec], error) {
	if err := validateJobID(jobID); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked(jobID)
}

/**
 * loadLocked reads and decodes the job for jobID; the caller must hold s.mu.
 *
 * Receiver:
 *   s *Store[Spec, Sec] - pointer receiver; the method may mutate this Store[Spec, Sec] instance
 *
 * Params:
 *   jobID string - the jobID string
 *
 * Returns:
 *   *Job[Spec] - the resulting *Job[Spec]
 *   error - error value; non-nil when the operation fails
 */
func (s *Store[Spec, Sec]) loadLocked(jobID string) (*Job[Spec], error) {
	data, err := os.ReadFile(s.path(jobID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("job %s not found", jobID)
		}
		return nil, fmt.Errorf("read job state: %w", err)
	}

	var job Job[Spec]
	if err := json.Unmarshal(data, &job); err != nil {
		return nil, fmt.Errorf("decode job state: %w", err)
	}
	return &job, nil
}

/**
 * LoadSecret reads and decodes the secret sidecar for jobID.
 *
 * Receiver:
 *   s *Store[Spec, Sec] - pointer receiver; the method may mutate this Store[Spec, Sec] instance
 *
 * Params:
 *   jobID string - the jobID string
 *
 * Returns:
 *   Sec - the resulting Sec
 *   error - error value; non-nil when the operation fails
 */
func (s *Store[Spec, Sec]) LoadSecret(jobID string) (Sec, error) {
	if err := validateJobID(jobID); err != nil {
		var zero Sec
		return zero, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var secret Sec
	data, err := os.ReadFile(s.secretPath(jobID))
	if err != nil {
		if os.IsNotExist(err) {
			return secret, fmt.Errorf("job %s secret not found", jobID)
		}
		return secret, fmt.Errorf("read job secret: %w", err)
	}
	if err := json.Unmarshal(data, &secret); err != nil {
		return secret, fmt.Errorf("decode job secret: %w", err)
	}
	return secret, nil
}

/**
 * List returns up to limit jobs, newest first by file modification time. A
 * non-positive limit returns all jobs. Unreadable/corrupt files are skipped.
 *
 * Receiver:
 *   s *Store[Spec, Sec] - pointer receiver; the method may mutate this Store[Spec, Sec] instance
 *
 * Params:
 *   limit int - the limit value
 *
 * Returns:
 *   []Job[Spec] - the resulting []Job[Spec]
 *   error - error value; non-nil when the operation fails
 */
func (s *Store[Spec, Sec]) List(limit int) ([]Job[Spec], error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("read state directory: %w", err)
	}

	type candidate struct {
		path    string
		modTime time.Time
	}
	candidates := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		if !isJobFile(entry.Name()) || entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{
			path:    filepath.Join(s.dir, entry.Name()),
			modTime: info.ModTime(),
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].modTime.After(candidates[j].modTime)
	})

	if limit > 0 && len(candidates) > limit {
		candidates = candidates[:limit]
	}

	jobs := make([]Job[Spec], 0, len(candidates))
	for _, c := range candidates {
		data, err := os.ReadFile(c.path)
		if err != nil {
			continue
		}
		var job Job[Spec]
		if err := json.Unmarshal(data, &job); err != nil {
			continue
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

/**
 * MarkStaleRunningJobsFailed rewrites any job left in the "running" state (e.g.
 * after a crash mid-deploy) to "failed", so jobs are never stuck running.
 *
 * Receiver:
 *   s *Store[Spec, Sec] - pointer receiver; the method may mutate this Store[Spec, Sec] instance
 */
func (s *Store[Spec, Sec]) MarkStaleRunningJobsFailed() {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !isJobFile(entry.Name()) || entry.IsDir() {
			continue
		}
		path := filepath.Join(s.dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var job Job[Spec]
		if err := json.Unmarshal(data, &job); err != nil {
			continue
		}
		if job.Status != JobStatusRunning {
			continue
		}
		job.Status = JobStatusFailed
		job.Error = "service restarted while job was in progress"
		job.UpdatedAt = time.Now().UTC()
		payload, err := json.MarshalIndent(&job, "", "  ")
		if err != nil {
			continue
		}
		_ = os.WriteFile(path, payload, 0o600)
	}
}

// MoveJobsTo copies every file-backed job and secret in this store to dest,
// then removes the source files after each job is safely persisted. Corrupt or
// unreadable files are skipped, matching List's existing tolerance.
func (s *Store[Spec, Sec]) MoveJobsTo(dest JobStore[Spec, Sec]) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("read state directory: %w", err)
	}
	for _, entry := range entries {
		if !isJobFile(entry.Name()) || entry.IsDir() {
			continue
		}
		jobID := strings.TrimSuffix(entry.Name(), ".json")
		if err := validateJobID(jobID); err != nil {
			continue
		}
		job, err := s.loadLocked(jobID)
		if err != nil {
			continue
		}
		if err := dest.Save(job); err != nil {
			return fmt.Errorf("migrate job %s: %w", jobID, err)
		}
		if secret, err := s.loadSecretLocked(jobID); err == nil {
			if err := dest.SaveSecret(jobID, secret); err != nil {
				return fmt.Errorf("migrate job %s secret: %w", jobID, err)
			}
			if err := os.Remove(s.secretPath(jobID)); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove migrated job %s secret: %w", jobID, err)
			}
		}
		if err := os.Remove(s.path(jobID)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove migrated job %s: %w", jobID, err)
		}
	}
	return nil
}

/**
 * isJobFile reports whether name is a job state file (and not a secret sidecar).
 *
 * Params:
 *   name string - the name string
 *
 * Returns:
 *   bool - boolean result
 */
func isJobFile(name string) bool {
	return strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, ".secret.json")
}

func (s *Store[Spec, Sec]) loadSecretLocked(jobID string) (Sec, error) {
	var secret Sec
	data, err := os.ReadFile(s.secretPath(jobID))
	if err != nil {
		if os.IsNotExist(err) {
			return secret, fmt.Errorf("job %s secret not found", jobID)
		}
		return secret, fmt.Errorf("read job secret: %w", err)
	}
	if err := json.Unmarshal(data, &secret); err != nil {
		return secret, fmt.Errorf("decode job secret: %w", err)
	}
	return secret, nil
}

/**
 * path.
 *
 * Receiver:
 *   s *Store[Spec, Sec] - pointer receiver; the method may mutate this Store[Spec, Sec] instance
 *
 * Params:
 *   jobID string - the jobID string
 *
 * Returns:
 *   string - the resulting string
 */
func (s *Store[Spec, Sec]) path(jobID string) string {
	return filepath.Join(s.dir, jobID+".json")
}

/**
 * secretPath.
 *
 * Receiver:
 *   s *Store[Spec, Sec] - pointer receiver; the method may mutate this Store[Spec, Sec] instance
 *
 * Params:
 *   jobID string - the jobID string
 *
 * Returns:
 *   string - the resulting string
 */
func (s *Store[Spec, Sec]) secretPath(jobID string) string {
	return filepath.Join(s.dir, jobID+".secret.json")
}
