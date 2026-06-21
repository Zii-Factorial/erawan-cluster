package mysql

import (
	"context"
	"fmt"
	"time"

	"erawan-cluster/internal/cluster/core"
)

type Service struct {
	// ctx is the long-lived base context for background jobs, which intentionally
	// outlive the originating HTTP request. It is set once at start-up via
	// SetContext to the process signal context, so a shutdown cancels in-flight
	// Ansible runs. It is never derived from a per-request context.
	ctx              context.Context
	store            *Store
	runner           *Runner
	collector        *Collector
	steps            []step
	sshUser          string
	sshKeyPath       string
	launcher         *core.Launcher
	start            func(func())
	runDeployStep    func(context.Context, runConfig) StepResult
	runRollbackStep  func(context.Context, string, StoredSpec, SecretInput, time.Duration) StepResult
	runAddMemberStep func(context.Context, memberRunConfig) StepResult
	runRemMemberStep func(context.Context, memberRunConfig) StepResult
}

// defaultMaxConcurrentJobs bounds concurrent background jobs until configured.
const defaultMaxConcurrentJobs = 4

type step = core.Step

func NewService(store *Store, runner *Runner) *Service {
	svc := &Service{
		ctx:       context.Background(),
		store:     store,
		runner:    runner,
		collector: NewCollector(),
		steps: []step{
			{Name: "preflight", Tag: "preflight"},
			{Name: "configure_instances", Tag: "configure_instances"},
			{Name: "create_cluster", Tag: "create_cluster"},
			{Name: "add_instances", Tag: "add_instances"},
			{Name: "enable_auto_rejoin", Tag: "enable_auto_rejoin"},
			{Name: "verify_cluster", Tag: "verify_cluster"},
			{Name: "init_app_db", Tag: "init_app_db"},
			{Name: "boot_recovery", Tag: "boot_recovery"},
			{Name: "bootstrap_router", Tag: "bootstrap_router", Skippable: true},
		},
	}
	svc.launcher = core.NewLauncher(defaultMaxConcurrentJobs)
	svc.start = svc.launcher.Go
	if runner != nil {
		svc.runDeployStep = runner.RunDeployStep
		svc.runRollbackStep = runner.RunRollback
		svc.runAddMemberStep = runner.RunAddMember
		svc.runRemMemberStep = runner.RunRemoveMember
	}
	return svc
}

func (s *Service) SetContext(ctx context.Context) {
	s.ctx = ctx
}

// SetMaxConcurrentJobs bounds how many background jobs run at once. It must be
// called at start-up, before any job is launched.
func (s *Service) SetMaxConcurrentJobs(n int) {
	s.launcher = core.NewLauncher(n)
	s.start = s.launcher.Go
}

// Wait blocks until all in-flight background jobs finish or ctx is done. Used
// during graceful shutdown to drain running jobs.
func (s *Service) Wait(ctx context.Context) {
	s.launcher.Wait(ctx)
}

func (s *Service) SetSSHConfig(user, privateKeyPath string) error {
	normalizedUser, normalizedKeyPath, err := ValidateServiceSSHConfig(user, privateKeyPath)
	if err != nil {
		return err
	}
	s.sshUser = normalizedUser
	s.sshKeyPath = normalizedKeyPath
	return nil
}

func (s *Service) Deploy(ctx context.Context, req DeployRequest) (*Job, error) {
	_ = ctx
	if err := ValidateDeployRequest(&req); err != nil {
		return nil, err
	}
	if err := s.hydrateStoredSSHConfig(nil); err != nil {
		return nil, err
	}

	job := &Job{
		ID:                newJobID(),
		Status:            JobStatusRunning,
		CreatedAt:         time.Now().UTC(),
		UpdatedAt:         time.Now().UTC(),
		LastCompletedStep: -1,
		Request: StoredSpec{
			AdminUsername:      req.AdminUsername,
			ClusterName:        req.ClusterName,
			PrimaryIP:          req.PrimaryIP,
			StandbyIPs:         req.StandbyIPs,
			NewUser:            req.NewUser,
			NewUserSSLRequired: req.NewUserSSLRequired,
			NewDB:              req.NewDB,
			AssumePrepared:     req.AssumePrepared,
			BootstrapRouter:    req.BootstrapRouterEnabled(),
			SSHUser:            s.sshUser,
			SSHPrivateKeyPath:  s.sshKeyPath,
			SSHPort:            req.SSHPort,
			MySQLPort:          req.MySQLPort,
			MySQLVersion:       req.MySQLVersion,
			StepTimeoutSeconds: req.StepTimeoutSeconds,
		},
		Steps: make([]StepResult, 0, len(s.steps)+1),
	}
	s.updateJobProgress(job)

	if err := s.store.Save(job); err != nil {
		return nil, err
	}

	secrets := SecretInput{
		RootPassword:    req.RootPassword,
		AdminPassword:   stringOrGenerated(req.AdminPassword),
		NewUserPassword: req.NewUserPassword,
	}
	if err := s.store.SaveSecret(job.ID, StoredSecret{AdminUser: req.AdminUsername, AdminPassword: secrets.AdminPassword}); err != nil {
		return nil, err
	}

	bgJob, err := s.store.Load(job.ID)
	if err != nil {
		return nil, err
	}
	s.start(func() {
		_ = s.executeFrom(s.ctx, bgJob, 0, secrets)
	})
	return job, nil
}

func (s *Service) Resume(ctx context.Context, jobID string, req ResumeRequest) (*Job, error) {
	_ = ctx
	secret, err := ValidateResumeSecrets(req)
	if err != nil {
		return nil, err
	}

	job, err := s.store.Load(jobID)
	if err != nil {
		return nil, err
	}
	if err := s.hydrateStoredSSHConfig(job); err != nil {
		return nil, err
	}
	if job.Status == JobStatusCompleted {
		return nil, fmt.Errorf("job %s already completed", jobID)
	}
	if job.Status == JobStatusRolledBack {
		return nil, fmt.Errorf("job %s already rolled back", jobID)
	}
	if job.Status == JobStatusRunning {
		return nil, fmt.Errorf("job %s is already running", jobID)
	}

	startIndex := job.LastCompletedStep + 1
	if startIndex >= len(s.steps) {
		job.Status = JobStatusCompleted
		job.Error = ""
		s.updateJobProgress(job)
		_ = s.store.Save(job)
		return job, nil
	}
	if job.Request.NewUser != "" && secret.NewUserPassword == "" {
		return nil, fmt.Errorf("new_user_password is required to resume job %s", jobID)
	}
	if secret.AdminPassword == "" {
		storedSecret, err := s.store.LoadSecret(job.ID)
		if err == nil && storedSecret.AdminPassword != "" {
			secret.AdminPassword = storedSecret.AdminPassword
		} else {
			secret.AdminPassword = stringOrGenerated("")
			if saveErr := s.store.SaveSecret(job.ID, StoredSecret{AdminUser: job.Request.AdminUsername, AdminPassword: secret.AdminPassword}); saveErr != nil {
				return nil, saveErr
			}
		}
	}

	job.Status = JobStatusRunning
	job.Error = ""
	s.updateJobProgress(job)
	if err := s.store.Save(job); err != nil {
		return nil, err
	}

	bgJob, err := s.store.Load(job.ID)
	if err != nil {
		return nil, err
	}
	s.start(func() {
		_ = s.executeFrom(s.ctx, bgJob, startIndex, secret)
	})
	return job, nil
}

func (s *Service) Rollback(ctx context.Context, jobID string, req RollbackRequest) (*Job, error) {
	secret, err := ValidateRollbackSecrets(req)
	if err != nil {
		return nil, err
	}
	job, err := s.store.Load(jobID)
	if err != nil {
		return nil, err
	}
	if err := s.hydrateStoredSSHConfig(job); err != nil {
		return nil, err
	}
	if storedSecret, err := s.store.LoadSecret(job.ID); err == nil && storedSecret.AdminPassword != "" {
		secret.AdminPassword = storedSecret.AdminPassword
	}

	timeout := time.Duration(job.Request.StepTimeoutSeconds) * time.Second
	result := s.runRollback(ctx, job.ID, job.Request, secret, timeout)
	job.Steps = append(job.Steps, result)

	if result.Status == JobStatusCompleted {
		job.Status = JobStatusRolledBack
		job.Error = ""
	} else {
		job.Status = JobStatusFailed
		job.Error = result.Message
	}
	s.updateJobProgress(job)
	if err := s.store.Save(job); err != nil {
		return nil, err
	}
	return job, nil
}

func (s *Service) AddMember(ctx context.Context, req AddMemberRequest) (*Job, error) {
	_ = ctx
	if err := ValidateAddMemberRequest(&req); err != nil {
		return nil, err
	}
	deployJob, err := s.store.Load(req.JobID)
	if err != nil {
		return nil, fmt.Errorf("load job %q: %w", req.JobID, err)
	}
	if err := s.hydrateStoredSSHConfig(deployJob); err != nil {
		return nil, err
	}

	existing := make(map[string]struct{}, len(deployJob.Request.StandbyIPs)+1)
	existing[deployJob.Request.PrimaryIP] = struct{}{}
	for _, ip := range deployJob.Request.StandbyIPs {
		existing[ip] = struct{}{}
	}
	for _, ip := range req.MemberIPs {
		if _, ok := existing[ip]; ok {
			return nil, fmt.Errorf("member_ip %s is already in the cluster", ip)
		}
	}

	if _, err := s.store.LoadSecret(req.JobID); err != nil {
		return nil, fmt.Errorf("load job secret %q: %w", req.JobID, err)
	}

	memberJob := &Job{
		ID:                newJobID(),
		Status:            JobStatusRunning,
		CreatedAt:         time.Now().UTC(),
		UpdatedAt:         time.Now().UTC(),
		LastCompletedStep: -1,
		Request:           deployJob.Request,
		MemberOp: &MemberOperation{
			Type:        "add",
			MemberIPs:   req.MemberIPs,
			SourceJobID: req.JobID,
		},
		Steps: make([]StepResult, 0, len(req.MemberIPs)),
	}
	s.updateJobProgress(memberJob)
	if err := s.store.Save(memberJob); err != nil {
		return nil, err
	}

	bgMemberJob, err := s.store.Load(memberJob.ID)
	if err != nil {
		return nil, err
	}
	bgDeployJob, err := s.store.Load(req.JobID)
	if err != nil {
		return nil, err
	}
	s.start(func() {
		s.executeMemberAdd(s.ctx, bgMemberJob, bgDeployJob)
	})
	return memberJob, nil
}

func (s *Service) RemoveMember(ctx context.Context, req RemoveMemberRequest) (*Job, error) {
	_ = ctx
	if err := ValidateRemoveMemberRequest(&req); err != nil {
		return nil, err
	}
	deployJob, err := s.store.Load(req.JobID)
	if err != nil {
		return nil, fmt.Errorf("load job %q: %w", req.JobID, err)
	}
	if err := s.hydrateStoredSSHConfig(deployJob); err != nil {
		return nil, err
	}

	if deployJob.Request.PrimaryIP == req.MemberIP {
		return nil, fmt.Errorf("cannot remove the primary node %s; promote a standby first", req.MemberIP)
	}
	found := false
	for _, ip := range deployJob.Request.StandbyIPs {
		if ip == req.MemberIP {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("member_ip %s is not in the cluster", req.MemberIP)
	}

	if _, err := s.store.LoadSecret(req.JobID); err != nil {
		return nil, fmt.Errorf("load job secret %q: %w", req.JobID, err)
	}

	memberJob := &Job{
		ID:                newJobID(),
		Status:            JobStatusRunning,
		CreatedAt:         time.Now().UTC(),
		UpdatedAt:         time.Now().UTC(),
		LastCompletedStep: -1,
		Request:           deployJob.Request,
		MemberOp: &MemberOperation{
			Type:        "remove",
			MemberIPs:   []string{req.MemberIP},
			SourceJobID: req.JobID,
		},
		Steps: make([]StepResult, 0, 1),
	}
	s.updateJobProgress(memberJob)
	if err := s.store.Save(memberJob); err != nil {
		return nil, err
	}

	bgMemberJob, err := s.store.Load(memberJob.ID)
	if err != nil {
		return nil, err
	}
	bgDeployJob, err := s.store.Load(req.JobID)
	if err != nil {
		return nil, err
	}
	force := req.Force
	s.start(func() {
		s.executeMemberRemove(s.ctx, bgMemberJob, bgDeployJob, force)
	})
	return memberJob, nil
}

func (s *Service) executeMemberAdd(ctx context.Context, memberJob *Job, deployJob *Job) {
	storedSecret, err := s.store.LoadSecret(deployJob.ID)
	if err != nil {
		memberJob.Status = JobStatusFailed
		memberJob.Error = fmt.Sprintf("load job secret: %s", err)
		s.updateJobProgress(memberJob)
		_ = s.store.Save(memberJob)
		return
	}
	secret := SecretInput{AdminPassword: storedSecret.AdminPassword}
	timeout := time.Duration(deployJob.Request.StepTimeoutSeconds) * time.Second

	for _, ip := range memberJob.MemberOp.MemberIPs {
		memberJob.CurrentStep = ip
		s.updateJobProgress(memberJob)
		_ = s.store.Save(memberJob)

		result := s.doAddMember(ctx, memberRunConfig{
			jobID:    deployJob.ID,
			spec:     deployJob.Request,
			secret:   secret,
			memberIP: ip,
			timeout:  timeout,
		})
		memberJob.Steps = append(memberJob.Steps, result)

		if result.Status == JobStatusCompleted {
			// Re-read and persist the deploy job under the store lock so a
			// concurrent member operation on the same cluster cannot clobber the
			// standby list.
			_ = s.store.Update(deployJob.ID, func(j *Job) error {
				j.Request.StandbyIPs = append(j.Request.StandbyIPs, ip)
				deployJob.Request.StandbyIPs = j.Request.StandbyIPs
				return nil
			})
			memberJob.Request.StandbyIPs = deployJob.Request.StandbyIPs
		} else {
			memberJob.Status = JobStatusFailed
			memberJob.Error = result.Message
			if memberJob.Error == "" {
				memberJob.Error = fmt.Sprintf("add member %s failed", ip)
			}
			memberJob.CurrentStep = ""
			s.updateJobProgress(memberJob)
			_ = s.store.Save(memberJob)
			return
		}
	}

	memberJob.Status = JobStatusCompleted
	memberJob.CurrentStep = ""
	memberJob.Error = ""
	s.updateJobProgress(memberJob)
	_ = s.store.Save(memberJob)
}

func (s *Service) executeMemberRemove(ctx context.Context, memberJob *Job, deployJob *Job, force bool) {
	storedSecret, err := s.store.LoadSecret(deployJob.ID)
	if err != nil {
		memberJob.Status = JobStatusFailed
		memberJob.Error = fmt.Sprintf("load job secret: %s", err)
		s.updateJobProgress(memberJob)
		_ = s.store.Save(memberJob)
		return
	}
	secret := SecretInput{AdminPassword: storedSecret.AdminPassword}
	timeout := time.Duration(deployJob.Request.StepTimeoutSeconds) * time.Second
	ip := memberJob.MemberOp.MemberIPs[0]

	memberJob.CurrentStep = ip
	s.updateJobProgress(memberJob)
	_ = s.store.Save(memberJob)

	result := s.doRemoveMember(ctx, memberRunConfig{
		jobID:    deployJob.ID,
		spec:     deployJob.Request,
		secret:   secret,
		memberIP: ip,
		force:    force,
		timeout:  timeout,
	})
	memberJob.Steps = append(memberJob.Steps, result)

	if result.Status == JobStatusCompleted {
		// Re-read and persist the deploy job under the store lock so a concurrent
		// member operation on the same cluster cannot clobber the standby list.
		_ = s.store.Update(deployJob.ID, func(j *Job) error {
			j.Request.StandbyIPs = without(j.Request.StandbyIPs, ip)
			deployJob.Request.StandbyIPs = j.Request.StandbyIPs
			return nil
		})
		memberJob.Request.StandbyIPs = deployJob.Request.StandbyIPs
		memberJob.Status = JobStatusCompleted
		memberJob.Error = ""
	} else {
		memberJob.Status = JobStatusFailed
		memberJob.Error = result.Message
		if memberJob.Error == "" {
			memberJob.Error = fmt.Sprintf("remove member %s failed", ip)
		}
	}
	memberJob.CurrentStep = ""
	s.updateJobProgress(memberJob)
	_ = s.store.Save(memberJob)
}

func (s *Service) doAddMember(ctx context.Context, cfg memberRunConfig) StepResult {
	if s.runAddMemberStep == nil {
		return StepResult{
			Name:      "add_member",
			Status:    JobStatusFailed,
			StartedAt: time.Now().UTC(),
			EndedAt:   time.Now().UTC(),
			ExitCode:  -1,
			Message:   "add member runner is not configured",
		}
	}
	return s.runAddMemberStep(ctx, cfg)
}

func (s *Service) doRemoveMember(ctx context.Context, cfg memberRunConfig) StepResult {
	if s.runRemMemberStep == nil {
		return StepResult{
			Name:      "remove_member",
			Status:    JobStatusFailed,
			StartedAt: time.Now().UTC(),
			EndedAt:   time.Now().UTC(),
			ExitCode:  -1,
			Message:   "remove member runner is not configured",
		}
	}
	return s.runRemMemberStep(ctx, cfg)
}

func (s *Service) Get(jobID string) (*Job, error) {
	job, err := s.store.Load(jobID)
	if err != nil {
		return nil, err
	}
	s.updateJobProgress(job)
	return job, nil
}

func (s *Service) GetSecret(jobID string) (*StoredSecret, error) {
	secret, err := s.store.LoadSecret(jobID)
	if err != nil {
		return nil, err
	}
	return &secret, nil
}

func (s *Service) List(limit int) ([]Job, error) {
	jobs, err := s.store.List(limit)
	if err != nil {
		return nil, err
	}
	for i := range jobs {
		s.updateJobProgress(&jobs[i])
	}
	return jobs, nil
}

func (s *Service) hydrateStoredSSHConfig(job *Job) error {
	if s.sshUser == "" || s.sshKeyPath == "" {
		return fmt.Errorf("ssh service configuration is incomplete")
	}
	if job != nil {
		if job.Request.SSHUser == "" {
			job.Request.SSHUser = s.sshUser
		}
		if job.Request.SSHPrivateKeyPath == "" {
			job.Request.SSHPrivateKeyPath = s.sshKeyPath
		}
	}
	return nil
}

func (s *Service) executeFrom(ctx context.Context, job *Job, startIndex int, secret SecretInput) error {
	timeout := time.Duration(job.Request.StepTimeoutSeconds) * time.Second
	for i := startIndex; i < len(s.steps); i++ {
		st := s.steps[i]
		if reason, shouldSkip := shouldSkipStep(st, job.Request); shouldSkip {
			job.LastCompletedStep = i
			job.CurrentStep = st.Name
			job.Steps = append(job.Steps, StepResult{
				Name:      st.Name,
				Status:    "skipped",
				StartedAt: time.Now().UTC(),
				EndedAt:   time.Now().UTC(),
				ExitCode:  0,
				Message:   reason,
			})
			s.updateJobProgress(job)
			if err := s.store.Save(job); err != nil {
				return err
			}
			continue
		}

		job.CurrentStep = st.Name
		s.updateJobProgress(job)
		if err := s.store.Save(job); err != nil {
			return err
		}

		res := s.runDeploy(ctx, runConfig{
			jobID:   job.ID,
			spec:    job.Request,
			secret:  secret,
			step:    st,
			timeout: timeout,
		})
		job.Steps = append(job.Steps, res)

		if res.Status != JobStatusCompleted {
			job.Status = JobStatusFailed
			job.Error = res.Message
			if job.Error == "" {
				job.Error = fmt.Sprintf("step %s failed", st.Name)
			}
			s.updateJobProgress(job)
			_ = s.store.Save(job)
			return fmt.Errorf("%s", job.Error)
		}

		job.LastCompletedStep = i
		job.Error = ""
		s.updateJobProgress(job)
		if err := s.store.Save(job); err != nil {
			return err
		}
	}

	job.Status = JobStatusCompleted
	job.CurrentStep = ""
	job.Error = ""
	s.updateJobProgress(job)
	if err := s.store.Save(job); err != nil {
		return err
	}
	return nil
}

func (s *Service) runDeploy(ctx context.Context, cfg runConfig) StepResult {
	if s.runDeployStep == nil {
		return StepResult{
			Name:      cfg.step.Name,
			Status:    JobStatusFailed,
			StartedAt: time.Now().UTC(),
			EndedAt:   time.Now().UTC(),
			ExitCode:  -1,
			Message:   "deploy runner is not configured",
		}
	}
	return s.runDeployStep(ctx, cfg)
}

func (s *Service) runRollback(ctx context.Context, jobID string, spec StoredSpec, secret SecretInput, timeout time.Duration) StepResult {
	if s.runRollbackStep == nil {
		return StepResult{
			Name:      "rollback",
			Status:    JobStatusFailed,
			StartedAt: time.Now().UTC(),
			EndedAt:   time.Now().UTC(),
			ExitCode:  -1,
			Message:   "rollback runner is not configured",
		}
	}
	return s.runRollbackStep(ctx, jobID, spec, secret, timeout)
}

func (s *Service) updateJobProgress(job *Job) {
	total := s.totalStepsFor(job.Request)
	if job.MemberOp != nil {
		total = len(job.MemberOp.MemberIPs)
	}
	core.ApplyProgress(job, total)
}

func (s *Service) totalStepsFor(spec StoredSpec) int {
	total := 0
	for _, st := range s.steps {
		if _, skip := shouldSkipStep(st, spec); skip {
			continue
		}
		total++
	}
	return total
}

func (s *Service) CollectMetrics(ctx context.Context, req MetricRequest) MetricResponse {
	return s.collector.Collect(ctx, req)
}

func (s *Service) ConnectionInfo(jobID string) (host string, port int, user, password string, err error) {
	job, err := s.store.Load(jobID)
	if err != nil {
		return "", 0, "", "", fmt.Errorf("load job %q: %w", jobID, err)
	}
	secret, err := s.store.LoadSecret(jobID)
	if err != nil {
		return "", 0, "", "", fmt.Errorf("load job secret %q: %w", jobID, err)
	}
	p := job.Request.MySQLPort
	if p == 0 {
		p = 3306
	}
	return job.Request.PrimaryIP, p, secret.AdminUser, secret.AdminPassword, nil
}

func shouldSkipStep(st step, spec StoredSpec) (string, bool) {
	if st.Name == "add_instances" && len(spec.StandbyIPs) == 0 {
		return "standby_ips is empty", true
	}
	if st.Name == "init_app_db" && (spec.NewUser == "" || spec.NewDB == "") {
		return "new_user/new_db not provided", true
	}
	if st.Skippable && !spec.BootstrapRouter {
		return "bootstrap_router is false", true
	}
	if spec.AssumePrepared && (st.Name == "preflight" || st.Name == "configure_instances") {
		return "assume_prepared is true", true
	}
	return "", false
}

// without returns slice with every occurrence of target removed.
func without(slice []string, target string) []string {
	out := make([]string, 0, len(slice))
	for _, v := range slice {
		if v != target {
			out = append(out, v)
		}
	}
	return out
}

func newJobID() string { return core.NewJobID() }

func stringOrGenerated(value string) string { return core.OrRandomSecret(value) }
