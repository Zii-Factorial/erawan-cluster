// Package mysql_test holds black-box unit tests for the MySQL cluster service
// and its database manager, exercising only their exported API.
package mysql_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"erawan-cluster/internal/cluster/core"
	mysql "erawan-cluster/internal/cluster/mysql"
	dbmanager "erawan-cluster/internal/cluster/mysql/dbmanager"
)

func tempKey(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(p, []byte("test-key"), 0o600); err != nil {
		t.Fatalf("write temp key: %v", err)
	}
	return p
}

func newService(t *testing.T) (*mysql.Service, *mysql.Store) {
	t.Helper()
	store, err := mysql.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	svc := mysql.NewService(store, nil) // nil runner: no real ansible is executed
	if err := svc.SetSSHConfig("clusterops", tempKey(t)); err != nil {
		t.Fatalf("set ssh config: %v", err)
	}
	return svc, store
}

func TestValidateDeployRequestAppliesDefaults(t *testing.T) {
	req := mysql.DeployRequest{ClusterName: "prodCluster", PrimaryIP: "10.0.0.1"}
	if err := mysql.ValidateDeployRequest(&req); err != nil {
		t.Fatalf("expected valid request, got %v", err)
	}
	if req.SSHPort != 22 || req.MySQLPort != 3306 || req.MySQLVersion != 8 {
		t.Fatalf("unexpected defaults: ssh=%d mysql=%d ver=%d", req.SSHPort, req.MySQLPort, req.MySQLVersion)
	}
	if req.AdminUsername != "clusteradmin" {
		t.Fatalf("expected default admin username, got %q", req.AdminUsername)
	}
}

func TestValidateDeployRequestRejectsBadInput(t *testing.T) {
	cases := map[string]mysql.DeployRequest{
		"missing cluster name": {PrimaryIP: "10.0.0.1"},
		"bad primary ip":       {ClusterName: "c", PrimaryIP: "not-an-ip"},
		"bad standby ip":       {ClusterName: "c", PrimaryIP: "10.0.0.1", StandbyIPs: []string{"x"}},
	}
	for name, req := range cases {
		req := req
		t.Run(name, func(t *testing.T) {
			if err := mysql.ValidateDeployRequest(&req); err == nil {
				t.Fatalf("expected validation error for %s", name)
			}
		})
	}
}

func TestValidateMemberRequests(t *testing.T) {
	if err := mysql.ValidateAddMemberRequest(&mysql.AddMemberRequest{}); err == nil {
		t.Fatal("expected error for empty add-member request")
	}
	add := &mysql.AddMemberRequest{JobID: "j", MemberIPs: []string{"10.0.0.5"}}
	if err := mysql.ValidateAddMemberRequest(add); err != nil {
		t.Fatalf("expected valid add-member request, got %v", err)
	}
	if err := mysql.ValidateRemoveMemberRequest(&mysql.RemoveMemberRequest{JobID: "j", MemberIP: "bad"}); err == nil {
		t.Fatal("expected error for invalid remove-member IP")
	}
}

func TestSetSSHConfigRejectsInvalidUser(t *testing.T) {
	svc := mysql.NewService(nil, nil)
	if err := svc.SetSSHConfig("bad user!", tempKey(t)); err == nil {
		t.Fatal("expected invalid ssh user to be rejected")
	}
}

func TestDeployPersistsRunningJobAndSecret(t *testing.T) {
	svc, store := newService(t)
	job, err := svc.Deploy(context.Background(), mysql.DeployRequest{
		ClusterName:        "prodCluster",
		PrimaryIP:          "10.0.0.1",
		StepTimeoutSeconds: 30,
	})
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	defer svc.Wait(context.Background()) // drain the background goroutine

	if job.Status != mysql.JobStatusRunning || job.ID == "" {
		t.Fatalf("expected a running job with an id, got status=%q id=%q", job.Status, job.ID)
	}
	if _, err := store.Load(job.ID); err != nil {
		t.Fatalf("expected job persisted: %v", err)
	}
	secret, err := svc.GetSecret(job.ID)
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if secret.AdminPassword == "" {
		t.Fatal("expected an admin password to be generated and stored")
	}
}

func TestDeployRejectsInvalidRequest(t *testing.T) {
	svc, _ := newService(t)
	if _, err := svc.Deploy(context.Background(), mysql.DeployRequest{}); err == nil {
		t.Fatal("expected deploy to reject an invalid request")
	}
}

func TestGetComputesProgressWithSkippedSteps(t *testing.T) {
	svc, store := newService(t)
	job := &mysql.Job{
		ID:     "j1",
		Status: mysql.JobStatusRunning,
		Request: mysql.StoredSpec{
			AssumePrepared:  true,
			BootstrapRouter: false,
		},
		Steps: []mysql.StepResult{
			{Name: "preflight", Status: core.JobStatusSkipped},
			{Name: "configure_instances", Status: core.JobStatusSkipped},
			{Name: "create_cluster", Status: mysql.JobStatusCompleted},
			{Name: "add_instances", Status: core.JobStatusSkipped},
		},
	}
	if err := store.Save(job); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := svc.Get("j1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.TotalSteps != 4 || got.CompletedSteps != 1 || got.ProgressPercent != 25 {
		t.Fatalf("unexpected progress: total=%d completed=%d pct=%d", got.TotalSteps, got.CompletedSteps, got.ProgressPercent)
	}
}

func TestListReturnsSeededJobs(t *testing.T) {
	svc, store := newService(t)
	_ = store.Save(&mysql.Job{ID: "a", Status: mysql.JobStatusCompleted})
	_ = store.Save(&mysql.Job{ID: "b", Status: mysql.JobStatusCompleted})
	jobs, err := svc.List(10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}
}

func TestConnectionInfoFromStoredJob(t *testing.T) {
	svc, store := newService(t)
	_ = store.Save(&mysql.Job{ID: "j", Status: mysql.JobStatusCompleted, Request: mysql.StoredSpec{PrimaryIP: "10.0.0.9", MySQLPort: 3307}})
	_ = store.SaveSecret("j", mysql.StoredSecret{AdminUser: "clusteradmin", AdminPassword: "pw"})

	host, port, user, pass, err := svc.ConnectionInfo("j")
	if err != nil {
		t.Fatalf("connection info: %v", err)
	}
	if host != "10.0.0.9" || port != 3307 || user != "clusteradmin" || pass != "pw" {
		t.Fatalf("unexpected connection info: %s:%d %s/%s", host, port, user, pass)
	}
}

// ── database manager (validation through the public API) ─────────────────────

func TestDBManagerRejectsInvalidRequests(t *testing.T) {
	store, err := mysql.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	db := dbmanager.NewService(store)
	ctx := context.Background()

	if err := db.CreateUser(ctx, dbmanager.CreateUserRequest{}); err == nil {
		t.Fatal("expected create-user to require job_id")
	}
	if err := db.CreateUser(ctx, dbmanager.CreateUserRequest{JobID: "j", Username: "ok"}); err == nil {
		t.Fatal("expected create-user to require a password")
	}
	if err := db.CreateDatabase(ctx, dbmanager.CreateDatabaseRequest{JobID: "j", DBName: "bad name!"}); err == nil {
		t.Fatal("expected create-database to reject an invalid name")
	}
	if err := db.DeleteUser(ctx, dbmanager.DeleteUserRequest{JobID: "j", Username: "bad user!"}); err == nil {
		t.Fatal("expected delete-user to reject an invalid username")
	}
}
