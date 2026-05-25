package pgsql

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Store struct {
	dir string
	mu  sync.Mutex
}

func NewStore(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("state directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	return &Store{dir: dir}, nil
}

func (s *Store) Save(job *Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	job.UpdatedAt = time.Now().UTC()
	payload, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal job: %w", err)
	}
	return os.WriteFile(s.path(job.ID), payload, 0o600)
}

func (s *Store) SaveSecret(jobID string, secret StoredSecret) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	payload, err := json.Marshal(secret)
	if err != nil {
		return fmt.Errorf("marshal job secret: %w", err)
	}
	return os.WriteFile(s.secretPath(jobID), payload, 0o600)
}

func (s *Store) Load(jobID string) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path(jobID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("job %s not found", jobID)
		}
		return nil, fmt.Errorf("read job state: %w", err)
	}

	var job Job
	if err := json.Unmarshal(data, &job); err != nil {
		return nil, fmt.Errorf("decode job state: %w", err)
	}
	return &job, nil
}

func (s *Store) LoadSecret(jobID string) (StoredSecret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.secretPath(jobID))
	if err != nil {
		if os.IsNotExist(err) {
			return StoredSecret{}, fmt.Errorf("job %s secret not found", jobID)
		}
		return StoredSecret{}, fmt.Errorf("read job secret: %w", err)
	}

	var secret StoredSecret
	if err := json.Unmarshal(data, &secret); err != nil {
		return StoredSecret{}, fmt.Errorf("decode job secret: %w", err)
	}
	return secret, nil
}

func (s *Store) List(limit int) ([]Job, error) {
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
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.HasSuffix(entry.Name(), ".secret.json") {
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

	jobs := make([]Job, 0, len(candidates))
	for _, c := range candidates {
		data, err := os.ReadFile(c.path)
		if err != nil {
			continue
		}
		var job Job
		if err := json.Unmarshal(data, &job); err != nil {
			continue
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func (s *Store) path(jobID string) string {
	return filepath.Join(s.dir, jobID+".json")
}

func (s *Store) secretPath(jobID string) string {
	return filepath.Join(s.dir, jobID+".secret.json")
}
