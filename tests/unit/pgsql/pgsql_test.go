// Package pgsql_test holds black-box unit tests for the PostgreSQL cluster
// service and its database manager, exercising only their exported API.
package pgsql_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"erawan-cluster/internal/cluster/core"
	pgsql "erawan-cluster/internal/cluster/pgsql"
	dbmanager "erawan-cluster/internal/cluster/pgsql/dbmanager"
)

func tempKey(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(p, []byte("test-key"), 0o600); err != nil {
		t.Fatalf("write temp key: %v", err)
	}
	return p
}

func newService(t *testing.T) (*pgsql.Service, *pgsql.Store) {
	t.Helper()
	store, err := pgsql.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	svc := pgsql.NewService(store, nil) // nil runner: no real ansible is executed
	if err := svc.SetSSHConfig("clusterops", tempKey(t)); err != nil {
		t.Fatalf("set ssh config: %v", err)
	}
	return svc, store
}

func TestValidateDeployRequestAppliesDefaults(t *testing.T) {
	req := pgsql.DeployRequest{PrimaryIP: "10.0.0.1"}
	if err := pgsql.ValidateDeployRequest(&req); err != nil {
		t.Fatalf("expected valid request, got %v", err)
	}
	if req.SSHPort != 22 || req.PostgresPort != 5432 || req.PostgresVersion != 16 {
		t.Fatalf("unexpected defaults: ssh=%d pg=%d ver=%d", req.SSHPort, req.PostgresPort, req.PostgresVersion)
	}
	if req.AdminUsername != "admin" || req.ClusterName != "postgres-cluster" {
		t.Fatalf("unexpected name defaults: admin=%q cluster=%q", req.AdminUsername, req.ClusterName)
	}
}

func TestValidateDeployRequestRejectsBadInput(t *testing.T) {
	cases := map[string]pgsql.DeployRequest{
		"bad primary ip":         {PrimaryIP: "not-an-ip"},
		"bad standby ip":         {PrimaryIP: "10.0.0.1", StandbyIPs: []string{"x"}},
		"unsupported pg version": {PrimaryIP: "10.0.0.1", PostgresVersion: 99},
	}
	for name, req := range cases {
		req := req
		t.Run(name, func(t *testing.T) {
			if err := pgsql.ValidateDeployRequest(&req); err == nil {
				t.Fatalf("expected validation error for %s", name)
			}
		})
	}
}

func TestValidateMemberRequests(t *testing.T) {
	if err := pgsql.ValidateAddMemberRequest(&pgsql.AddMemberRequest{}); err == nil {
		t.Fatal("expected error for empty add-member request")
	}
	add := &pgsql.AddMemberRequest{JobID: "j", MemberIPs: []string{"10.0.0.5"}}
	if err := pgsql.ValidateAddMemberRequest(add); err != nil {
		t.Fatalf("expected valid add-member request, got %v", err)
	}
	if err := pgsql.ValidateRemoveMemberRequest(&pgsql.RemoveMemberRequest{JobID: "j", MemberIP: "bad"}); err == nil {
		t.Fatal("expected error for invalid remove-member IP")
	}
}

func TestSetSSHConfigRejectsInvalidUser(t *testing.T) {
	svc := pgsql.NewService(nil, nil)
	if err := svc.SetSSHConfig("bad user!", tempKey(t)); err == nil {
		t.Fatal("expected invalid ssh user to be rejected")
	}
}

func TestDeployPersistsRunningJobAndSecret(t *testing.T) {
	svc, store := newService(t)
	job, err := svc.Deploy(context.Background(), pgsql.DeployRequest{
		ClusterName:        "prodcluster",
		PrimaryIP:          "10.0.0.1",
		StepTimeoutSeconds: 30,
	})
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	defer svc.Wait(context.Background())

	if job.Status != pgsql.JobStatusRunning || job.ID == "" {
		t.Fatalf("expected a running job with an id, got status=%q id=%q", job.Status, job.ID)
	}
	if _, err := store.Load(job.ID); err != nil {
		t.Fatalf("expected job persisted: %v", err)
	}
	secret, err := svc.GetSecret(job.ID)
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if secret.PostgresPassword == "" || secret.ReplicatorPassword == "" {
		t.Fatal("expected postgres and replicator passwords to be generated")
	}
}

func TestGetComputesProgressWithSkippedSteps(t *testing.T) {
	svc, store := newService(t)
	// Empty spec skips standby_config (no standbys) and init_app_db (no user/db):
	// 7 steps - 2 skipped = 5 applicable.
	job := &pgsql.Job{
		ID:     "j1",
		Status: pgsql.JobStatusRunning,
		Steps: []pgsql.StepResult{
			{Name: "preflight", Status: pgsql.JobStatusCompleted},
			{Name: "standby_config", Status: core.JobStatusSkipped},
		},
	}
	if err := store.Save(job); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := svc.Get("j1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.TotalSteps != 5 || got.CompletedSteps != 1 || got.ProgressPercent != 20 {
		t.Fatalf("unexpected progress: total=%d completed=%d pct=%d", got.TotalSteps, got.CompletedSteps, got.ProgressPercent)
	}
}

func TestConnectionInfoFromStoredJob(t *testing.T) {
	svc, store := newService(t)
	_ = store.Save(&pgsql.Job{ID: "j", Status: pgsql.JobStatusCompleted, Request: pgsql.StoredSpec{PrimaryIP: "10.0.0.9", PostgresPort: 5433, StandbyIPs: []string{"10.0.0.10"}}})
	_ = store.SaveSecret("j", pgsql.StoredSecret{PostgresUser: "postgres", PostgresPassword: "pw"})

	host, port, user, pass, nodeIPs, err := svc.ConnectionInfo(context.Background(), "j")
	if err != nil {
		t.Fatalf("connection info: %v", err)
	}
	if host != "10.0.0.9" || port != 5433 || user != "postgres" || pass != "pw" {
		t.Fatalf("unexpected connection info: %s:%d %s/%s", host, port, user, pass)
	}
	if len(nodeIPs) != 2 || nodeIPs[0] != "10.0.0.9" {
		t.Fatalf("expected primary + standby node IPs, got %v", nodeIPs)
	}
}

func TestDBManagerRejectsInvalidRequests(t *testing.T) {
	store, err := pgsql.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	db := dbmanager.NewService(store)
	ctx := context.Background()

	if err := db.CreateUser(ctx, dbmanager.CreateUserRequest{}); err == nil {
		t.Fatal("expected create-user to require job_id")
	}
	if err := db.CreateDatabase(ctx, dbmanager.CreateDatabaseRequest{JobID: "j", DBName: "bad name!"}); err == nil {
		t.Fatal("expected create-database to reject an invalid name")
	}
}
