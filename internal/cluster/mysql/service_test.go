package mysql

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestUpdateJobProgressCountsOnlyApplicableCompletedSteps(t *testing.T) {
	svc := NewService(nil, nil)
	job := &Job{
		Status: JobStatusRunning,
		Request: StoredSpec{
			AssumePrepared:  true,
			BootstrapRouter: false,
		},
		Steps: []StepResult{
			{Name: "preflight", Status: "skipped"},
			{Name: "configure_instances", Status: "skipped"},
			{Name: "create_cluster", Status: JobStatusCompleted},
			{Name: "add_instances", Status: "skipped"},
		},
	}

	svc.updateJobProgress(job)

	if job.TotalSteps != 4 {
		t.Fatalf("expected total_steps=4, got %d", job.TotalSteps)
	}
	if job.CompletedSteps != 1 {
		t.Fatalf("expected completed_steps=1, got %d", job.CompletedSteps)
	}
	if job.ProgressPercent != 25 {
		t.Fatalf("expected progress_percent=25, got %d", job.ProgressPercent)
	}
}

func TestUpdateJobProgressCompletedJobsReportOneHundredPercent(t *testing.T) {
	svc := NewService(nil, nil)
	job := &Job{
		Status: JobStatusCompleted,
		Request: StoredSpec{
			BootstrapRouter: true,
		},
	}

	svc.updateJobProgress(job)

	if job.TotalSteps != 7 {
		t.Fatalf("expected total_steps=7, got %d", job.TotalSteps)
	}
	if job.CompletedSteps != 7 {
		t.Fatalf("expected completed_steps=7, got %d", job.CompletedSteps)
	}
	if job.ProgressPercent != 100 {
		t.Fatalf("expected progress_percent=100, got %d", job.ProgressPercent)
	}
}

func TestDeploySchedulesBackgroundExecution(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	svc := NewService(store, nil)
	if err := svc.SetSSHConfig("clusterops", tempPrivateKeyPath(t)); err != nil {
		t.Fatalf("set ssh config: %v", err)
	}
	launched := false
	svc.start = func(fn func()) {
		launched = true
	}

	job, err := svc.Deploy(context.Background(), DeployRequest{
		ClusterName:        "prodCluster",
		PrimaryIP:          "10.0.0.1",
		StepTimeoutSeconds: 30,
	})
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if !launched {
		t.Fatal("expected deploy to launch background execution")
	}
	if job.Status != JobStatusRunning {
		t.Fatalf("expected running job status, got %q", job.Status)
	}
	if len(job.Steps) != 0 {
		t.Fatalf("expected no steps to run inline, got %d", len(job.Steps))
	}

	saved, err := store.Load(job.ID)
	if err != nil {
		t.Fatalf("load saved job: %v", err)
	}
	if saved.ID != job.ID {
		t.Fatalf("expected saved job id %q, got %q", job.ID, saved.ID)
	}
}

func TestValidateDeployRequestDefaultsSSHPort(t *testing.T) {
	req := DeployRequest{
		ClusterName: "prodCluster",
		PrimaryIP:   "10.0.0.1",
	}

	if err := ValidateDeployRequest(&req); err != nil {
		t.Fatalf("expected deploy request to validate, got %v", err)
	}
	if req.SSHPort != 22 {
		t.Fatalf("expected default ssh_port=22, got %d", req.SSHPort)
	}
}

func TestResumeSchedulesBackgroundExecution(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	svc := NewService(store, nil)
	if err := svc.SetSSHConfig("clusterops", tempPrivateKeyPath(t)); err != nil {
		t.Fatalf("set ssh config: %v", err)
	}
	launched := false
	svc.start = func(fn func()) {
		launched = true
	}

	job := &Job{
		ID:                "job1",
		Status:            JobStatusFailed,
		CreatedAt:         time.Now().UTC(),
		UpdatedAt:         time.Now().UTC(),
		LastCompletedStep: 0,
		Request: StoredSpec{
			AdminUsername: "clusteradmin",
			ClusterName:          "prodCluster",
			PrimaryIP:            "10.0.0.1",
			SSHUser:              "clusterops",
			SSHPrivateKeyPath:    "/tmp/test-key",
			SSHPort:              22,
			MySQLPort:            3306,
			StepTimeoutSeconds:   30,
		},
	}
	if err := store.Save(job); err != nil {
		t.Fatalf("save job: %v", err)
	}

	resumed, err := svc.Resume(context.Background(), job.ID, ResumeRequest{})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !launched {
		t.Fatal("expected resume to launch background execution")
	}
	if resumed.Status != JobStatusRunning {
		t.Fatalf("expected running job status, got %q", resumed.Status)
	}
}

func TestBuildInventoryYAMLUsesSSHKeyWhenProvided(t *testing.T) {
	inventory := buildInventoryYAML(StoredSpec{
		PrimaryIP:         "10.0.0.1",
		SSHUser:           "clusterops",
		SSHPrivateKeyPath: "/tmp/test-key",
		SSHPort:           22,
	})

	if strings.Contains(inventory, "ansible_password") {
		t.Fatalf("expected ssh key auth to omit ansible_password, inventory=%s", inventory)
	}
	if !strings.Contains(inventory, "ansible_ssh_private_key_file") {
		t.Fatalf("expected ssh key auth to include ansible_ssh_private_key_file, inventory=%s", inventory)
	}
}

func TestSetSSHConfigNormalizesPrivateKeyPath(t *testing.T) {
	svc := NewService(nil, nil)
	keyPath := tempPrivateKeyPath(t)

	if err := svc.SetSSHConfig("clusterops", keyPath); err != nil {
		t.Fatalf("set ssh config: %v", err)
	}
	if !strings.HasPrefix(svc.sshKeyPath, "/") {
		t.Fatalf("expected absolute ssh key path, got %q", svc.sshKeyPath)
	}
}

func tempPrivateKeyPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/id_ed25519"
	if err := os.WriteFile(path, []byte("test-private-key"), 0o600); err != nil {
		t.Fatalf("write temp private key: %v", err)
	}
	return path
}

func TestShouldSkipStepSkipsAddInstancesWhenNoStandbys(t *testing.T) {
	reason, skip := shouldSkipStep(step{Name: "add_instances"}, StoredSpec{})
	if !skip {
		t.Fatal("expected add_instances to be skipped when standby_ips is empty")
	}
	if reason != "standby_ips is empty" {
		t.Fatalf("unexpected skip reason: %q", reason)
	}
}
