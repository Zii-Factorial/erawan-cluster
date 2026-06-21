// Package core_test holds black-box unit tests for internal/cluster/core,
// exercising only its exported API.
package core_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"erawan-cluster/internal/cluster/core"
)

// spec and secret are minimal stand-ins for an engine's stored spec/secret.
type spec struct {
	Name string `json:"name"`
}

type secret struct {
	Pass string `json:"pass"`
}

func newStore(t *testing.T) *core.Store[spec, secret] {
	t.Helper()
	s, err := core.NewStore[spec, secret](t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return s
}

func TestStoreSaveLoadAndSecretRoundTrip(t *testing.T) {
	s := newStore(t)
	job := &core.Job[spec]{ID: "job1", Status: core.JobStatusRunning, Request: spec{Name: "cluster-a"}}
	if err := s.Save(job); err != nil {
		t.Fatalf("save: %v", err)
	}
	if job.UpdatedAt.IsZero() {
		t.Fatal("Save should stamp UpdatedAt")
	}
	if err := s.SaveSecret("job1", secret{Pass: "s3cr3t"}); err != nil {
		t.Fatalf("save secret: %v", err)
	}

	loaded, err := s.Load("job1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Request.Name != "cluster-a" {
		t.Fatalf("round-trip mismatch: %+v", loaded.Request)
	}
	sec, err := s.LoadSecret("job1")
	if err != nil {
		t.Fatalf("load secret: %v", err)
	}
	if sec.Pass != "s3cr3t" {
		t.Fatalf("secret round-trip mismatch: %q", sec.Pass)
	}
}

func TestStoreLoadMissingReturnsError(t *testing.T) {
	s := newStore(t)
	if _, err := s.Load("nope"); err == nil {
		t.Fatal("expected error loading missing job")
	}
	if _, err := s.LoadSecret("nope"); err == nil {
		t.Fatal("expected error loading missing secret")
	}
}

func TestStoreListNewestFirstAndLimit(t *testing.T) {
	s := newStore(t)
	for _, id := range []string{"a", "b", "c"} {
		if err := s.Save(&core.Job[spec]{ID: id, Status: core.JobStatusCompleted}); err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
		time.Sleep(10 * time.Millisecond) // distinct mod times for ordering
	}
	jobs, err := s.List(2)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected limit=2 jobs, got %d", len(jobs))
	}
	if jobs[0].ID != "c" {
		t.Fatalf("expected newest job 'c' first, got %q", jobs[0].ID)
	}
}

func TestStoreListExcludesSecretSidecars(t *testing.T) {
	s := newStore(t)
	if err := s.Save(&core.Job[spec]{ID: "x", Status: core.JobStatusCompleted}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := s.SaveSecret("x", secret{Pass: "p"}); err != nil {
		t.Fatalf("save secret: %v", err)
	}
	jobs, err := s.List(0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected secret sidecar excluded, got %d jobs", len(jobs))
	}
}

func TestStoreMarkStaleRunningJobsFailed(t *testing.T) {
	s := newStore(t)
	_ = s.Save(&core.Job[spec]{ID: "running", Status: core.JobStatusRunning})
	_ = s.Save(&core.Job[spec]{ID: "done", Status: core.JobStatusCompleted})

	s.MarkStaleRunningJobsFailed()

	r, _ := s.Load("running")
	if r.Status != core.JobStatusFailed {
		t.Fatalf("expected stale running job marked failed, got %q", r.Status)
	}
	if r.Error == "" {
		t.Fatal("expected an error message on the failed job")
	}
	d, _ := s.Load("done")
	if d.Status != core.JobStatusCompleted {
		t.Fatalf("completed job must be untouched, got %q", d.Status)
	}
}

func TestStoreUpdateIsAtomicReadModifyWrite(t *testing.T) {
	s := newStore(t)
	_ = s.Save(&core.Job[spec]{ID: "j", Status: core.JobStatusRunning, Request: spec{Name: "n0"}})

	if err := s.Update("j", func(j *core.Job[spec]) error {
		j.Request.Name = "n1"
		return nil
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	loaded, _ := s.Load("j")
	if loaded.Request.Name != "n1" {
		t.Fatalf("expected update persisted, got %q", loaded.Request.Name)
	}
}

func TestApplyProgressCountsCompletedSteps(t *testing.T) {
	job := &core.Job[spec]{
		Status: core.JobStatusRunning,
		Steps: []core.StepResult{
			{Status: core.JobStatusCompleted},
			{Status: core.JobStatusSkipped},
			{Status: core.JobStatusCompleted},
		},
	}
	core.ApplyProgress(job, 4)
	if job.TotalSteps != 4 || job.CompletedSteps != 2 || job.ProgressPercent != 50 {
		t.Fatalf("unexpected progress: total=%d completed=%d pct=%d", job.TotalSteps, job.CompletedSteps, job.ProgressPercent)
	}
}

func TestApplyProgressCompletedJobReportsFull(t *testing.T) {
	job := &core.Job[spec]{Status: core.JobStatusCompleted}
	core.ApplyProgress(job, 7)
	if job.CompletedSteps != 7 || job.ProgressPercent != 100 {
		t.Fatalf("completed job should report 100%%, got completed=%d pct=%d", job.CompletedSteps, job.ProgressPercent)
	}
}

func TestApplyProgressZeroStepsIsZeroPercent(t *testing.T) {
	job := &core.Job[spec]{Status: core.JobStatusRunning}
	core.ApplyProgress(job, 0)
	if job.ProgressPercent != 0 {
		t.Fatalf("expected 0%% for zero steps, got %d", job.ProgressPercent)
	}
}

func TestNewJobIDAndRandomSecret(t *testing.T) {
	if a, b := core.NewJobID(), core.NewJobID(); a == b || len(a) != 24 {
		t.Fatalf("job ids should be unique 24-hex strings, got %q %q", a, b)
	}
	if core.OrRandomSecret("given") != "given" {
		t.Fatal("OrRandomSecret should return a provided value unchanged")
	}
	if g := core.OrRandomSecret(""); len(g) != 48 {
		t.Fatalf("OrRandomSecret(\"\") should be 48 hex chars, got %d", len(g))
	}
}

func TestSSHPolicySecureByDefault(t *testing.T) {
	p := core.SSHPolicy{VerifyHostKeys: true, KnownHostsFile: "/etc/erawan/known_hosts"}
	if env := p.AnsibleEnv(); len(env) != 1 || env[0] != "ANSIBLE_HOST_KEY_CHECKING=True" {
		t.Fatalf("expected host key checking enabled, got %v", env)
	}
	args := p.SSHCommonArgs()
	if !strings.Contains(args, "StrictHostKeyChecking=yes") || !strings.Contains(args, "UserKnownHostsFile=/etc/erawan/known_hosts") {
		t.Fatalf("unexpected secure ssh args: %q", args)
	}
}

func TestSSHPolicyInsecureOptOut(t *testing.T) {
	p := core.SSHPolicy{VerifyHostKeys: false}
	if env := p.AnsibleEnv(); env[0] != "ANSIBLE_HOST_KEY_CHECKING=False" {
		t.Fatalf("expected host key checking disabled, got %v", env)
	}
	if args := p.SSHCommonArgs(); !strings.Contains(args, "StrictHostKeyChecking=no") {
		t.Fatalf("expected StrictHostKeyChecking=no, got %q", args)
	}
}

func TestLauncherCapsConcurrency(t *testing.T) {
	const limit = 2
	l := core.NewLauncher(limit)
	var current, peak int64
	var mu sync.Mutex
	release := make(chan struct{})

	for i := 0; i < 6; i++ {
		l.Go(func() {
			n := atomic.AddInt64(&current, 1)
			mu.Lock()
			if n > peak {
				peak = n
			}
			mu.Unlock()
			<-release
			atomic.AddInt64(&current, -1)
		})
	}
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt64(&current); got > limit {
		t.Fatalf("expected at most %d concurrent jobs, got %d", limit, got)
	}
	close(release)
	l.Wait(context.Background())
	if peak == 0 || peak > limit {
		t.Fatalf("peak concurrency %d outside (0, %d]", peak, limit)
	}
}

func TestLauncherWaitRespectsContext(t *testing.T) {
	l := core.NewLauncher(1)
	block := make(chan struct{})
	l.Go(func() { <-block })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	l.Wait(ctx)
	if time.Since(start) > time.Second {
		t.Fatal("Wait did not return when context expired")
	}
	close(block)
}

func TestAnsibleRunRejectsUnconfiguredPlaybook(t *testing.T) {
	res := core.AnsibleRun(context.Background(), core.AnsibleSpec{Bin: "true", StepName: "deploy"})
	if res.Status != core.JobStatusFailed || !strings.Contains(res.Message, "playbook path is not configured") {
		t.Fatalf("expected not-configured failure, got %+v", res)
	}
}

func TestAnsibleRunSuccessAndFailureExitCodes(t *testing.T) {
	ok := core.AnsibleRun(context.Background(), core.AnsibleSpec{
		Bin: "true", Playbook: "playbook.yml", StepName: "deploy", WorkspacePrefix: "core-test-",
	})
	if ok.Status != core.JobStatusCompleted || ok.ExitCode != 0 {
		t.Fatalf("expected success with /usr/bin/true, got %+v", ok)
	}
	fail := core.AnsibleRun(context.Background(), core.AnsibleSpec{
		Bin: "false", Playbook: "playbook.yml", StepName: "deploy", WorkspacePrefix: "core-test-",
	})
	if fail.Status != core.JobStatusFailed || fail.ExitCode == 0 {
		t.Fatalf("expected failure with /usr/bin/false, got %+v", fail)
	}
}
