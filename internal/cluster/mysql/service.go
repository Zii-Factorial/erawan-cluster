package mysql

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

type Service struct {
	ctx               context.Context
	store             *Store
	runner            *Runner
	collector         *Collector
	steps             []step
	sshUser           string
	sshKeyPath        string
	start             func(func())
	runDeployStep     func(context.Context, runConfig) StepResult
	runRollbackStep   func(context.Context, string, StoredSpec, SecretInput, time.Duration) StepResult
	runAddMemberStep  func(context.Context, memberRunConfig) StepResult
	runRemMemberStep  func(context.Context, memberRunConfig) StepResult
}

type step struct {
	Name      string
	Tag       string
	Skippable bool
}

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
	svc.start = func(fn func()) { go fn() }
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
			AdminUsername: req.AdminUsername,
			ClusterName:          req.ClusterName,
			PrimaryIP:            req.PrimaryIP,
			StandbyIPs:           req.StandbyIPs,
			NewUser:              req.NewUser,
			NewUserSSLRequired:   req.NewUserSSLRequired,
			NewDB:                req.NewDB,
			AssumePrepared:       req.AssumePrepared,
			BootstrapRouter:      req.BootstrapRouterEnabled(),
			SSHUser:              s.sshUser,
			SSHPrivateKeyPath:    s.sshKeyPath,
			SSHPort:              req.SSHPort,
			MySQLPort:            req.MySQLPort,
			MySQLVersion:         req.MySQLVersion,
			StepTimeoutSeconds:   req.StepTimeoutSeconds,
		},
		Steps: make([]StepResult, 0, len(s.steps)+1),
	}
	s.updateJobProgress(job)

	if err := s.store.Save(job); err != nil {
		return nil, err
	}

	secrets := SecretInput{
		RootPassword:         req.RootPassword,
		AdminPassword: stringOrGenerated(req.AdminPassword),
		NewUserPassword:      req.NewUserPassword,
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

func (s *Service) AddMember(ctx context.Context, req AddMemberRequest) (*MemberOperationResult, error) {
	if err := ValidateAddMemberRequest(&req); err != nil {
		return nil, err
	}
	job, err := s.store.Load(req.JobID)
	if err != nil {
		return nil, fmt.Errorf("load job %q: %w", req.JobID, err)
	}
	if err := s.hydrateStoredSSHConfig(job); err != nil {
		return nil, err
	}

	existing := make(map[string]struct{}, len(job.Request.StandbyIPs)+1)
	existing[job.Request.PrimaryIP] = struct{}{}
	for _, ip := range job.Request.StandbyIPs {
		existing[ip] = struct{}{}
	}
	for _, ip := range req.MemberIPs {
		if _, ok := existing[ip]; ok {
			return nil, fmt.Errorf("member_ip %s is already in the cluster", ip)
		}
	}

	storedSecret, err := s.store.LoadSecret(req.JobID)
	if err != nil {
		return nil, fmt.Errorf("load job secret %q: %w", req.JobID, err)
	}

	timeout := time.Duration(job.Request.StepTimeoutSeconds) * time.Second
	out := &MemberOperationResult{
		Action:    "add",
		MemberIPs: req.MemberIPs,
		Spec:      job.Request,
		Steps:     make([]StepResult, 0, len(req.MemberIPs)),
	}

	for _, ip := range req.MemberIPs {
		result := s.doAddMember(ctx, memberRunConfig{
			jobID:    req.JobID,
			spec:     job.Request,
			secret:   SecretInput{AdminPassword: storedSecret.AdminPassword},
			memberIP: ip,
			timeout:  timeout,
		})
		out.Steps = append(out.Steps, result)
		if result.Status == JobStatusCompleted {
			job.Request.StandbyIPs = append(job.Request.StandbyIPs, ip)
			_ = s.store.Save(job)
		} else {
			out.Spec = job.Request
			return out, stepError(result)
		}
	}

	out.Spec = job.Request
	return out, nil
}

func (s *Service) RemoveMember(ctx context.Context, jobID string, req RemoveMemberRequest) (*MemberOperationResult, error) {
	if err := ValidateRemoveMemberRequest(&req); err != nil {
		return nil, err
	}
	job, err := s.store.Load(jobID)
	if err != nil {
		return nil, fmt.Errorf("load job %q: %w", jobID, err)
	}
	if err := s.hydrateStoredSSHConfig(job); err != nil {
		return nil, err
	}

	if job.Request.PrimaryIP == req.MemberIP {
		return nil, fmt.Errorf("cannot remove the primary node %s; promote a standby first", req.MemberIP)
	}
	found := false
	for _, ip := range job.Request.StandbyIPs {
		if ip == req.MemberIP {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("member_ip %s is not in the cluster", req.MemberIP)
	}

	storedSecret, err := s.store.LoadSecret(jobID)
	if err != nil {
		return nil, fmt.Errorf("load job secret %q: %w", jobID, err)
	}
	adminPassword := strings.TrimSpace(req.AdminPassword)
	if adminPassword == "" {
		adminPassword = storedSecret.AdminPassword
	}

	timeout := time.Duration(job.Request.StepTimeoutSeconds) * time.Second
	result := s.doRemoveMember(ctx, memberRunConfig{
		jobID:    jobID,
		spec:     job.Request,
		secret:   SecretInput{AdminPassword: adminPassword},
		memberIP: req.MemberIP,
		force:    req.Force,
		timeout:  timeout,
	})

	if result.Status == JobStatusCompleted {
		updated := make([]string, 0, len(job.Request.StandbyIPs))
		for _, ip := range job.Request.StandbyIPs {
			if ip != req.MemberIP {
				updated = append(updated, ip)
			}
		}
		job.Request.StandbyIPs = updated
		_ = s.store.Save(job)
	}

	return &MemberOperationResult{
		Action:    "remove",
		MemberIPs: []string{req.MemberIP},
		Spec:      job.Request,
		Steps:     []StepResult{result},
	}, stepError(result)
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

func stepError(result StepResult) error {
	if result.Status != JobStatusCompleted {
		if result.Message != "" {
			return fmt.Errorf("%s", result.Message)
		}
		return fmt.Errorf("step %s failed", result.Name)
	}
	return nil
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
	job.TotalSteps = s.totalStepsFor(job.Request)
	job.CompletedSteps = completedSteps(job)
	if job.Status == JobStatusCompleted && job.TotalSteps > 0 {
		job.CompletedSteps = job.TotalSteps
	}
	if job.CompletedSteps > job.TotalSteps {
		job.CompletedSteps = job.TotalSteps
	}
	if job.CompletedSteps < 0 || job.TotalSteps == 0 {
		job.ProgressPercent = 0
		return
	}
	job.ProgressPercent = job.CompletedSteps * 100 / job.TotalSteps
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

func completedSteps(job *Job) int {
	count := 0
	for _, step := range job.Steps {
		if step.Status == JobStatusCompleted {
			count++
		}
	}
	return count
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

func newJobID() string {
	raw := make([]byte, 12)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}

func stringOrGenerated(value string) string {
	if value != "" {
		return value
	}
	raw := make([]byte, 24)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}
