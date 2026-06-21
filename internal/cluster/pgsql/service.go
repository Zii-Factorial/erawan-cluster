package pgsql

import (
	"context"
	"fmt"
	"strings"
	"sync"
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
	runAddMemberStep func(context.Context, memberRunConfig) StepResult
	runRemMemberStep func(context.Context, memberRunConfig) StepResult
}

type step = core.Step

// defaultMaxConcurrentJobs bounds concurrent background jobs until configured.
const defaultMaxConcurrentJobs = 4

func NewService(store *Store, runner *Runner) *Service {
	svc := &Service{
		ctx:       context.Background(),
		store:     store,
		runner:    runner,
		collector: NewCollector(),
		steps: []step{
			{Name: "preflight", Tag: "preflight"},
			{Name: "base_config", Tag: "base_config"},
			{Name: "primary_config", Tag: "primary_config"},
			{Name: "standby_config", Tag: "standby_config"},
			{Name: "cluster_bootstrap", Tag: "cluster_bootstrap"},
			{Name: "verify_cluster", Tag: "verify_cluster"},
			{Name: "init_app_db", Tag: "init_app_db", Skippable: true},
		},
	}
	svc.launcher = core.NewLauncher(defaultMaxConcurrentJobs)
	svc.start = svc.launcher.Go
	if runner != nil {
		svc.runDeployStep = runner.RunDeployStep
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
			ClusterName:        req.ClusterName,
			PrimaryIP:          req.PrimaryIP,
			StandbyIPs:         req.StandbyIPs,
			AdminUsername:      req.AdminUsername,
			NewUser:            req.NewUser,
			NewUserSSLRequired: req.NewUserSSLRequiredEnabled(),
			NewDB:              req.NewDB,
			SSHUser:            s.sshUser,
			SSHPrivateKeyPath:  s.sshKeyPath,
			SSHPort:            req.SSHPort,
			PostgresPort:       req.PostgresPort,
			PostgresVersion:    req.PostgresVersion,
			StepTimeoutSeconds: req.StepTimeoutSeconds,
		},
		Steps: make([]StepResult, 0, len(s.steps)),
	}

	s.updateJobProgress(job)
	if err := s.store.Save(job); err != nil {
		return nil, err
	}

	secrets := SecretInput{
		PostgresPassword:   stringOrGenerated(req.PostgresPassword),
		ReplicatorPassword: stringOrGenerated(req.ReplicatorPassword),
		AdminPassword:      stringOrGenerated(req.AdminPassword),
		NewUserPassword:    req.NewUserPassword,
	}
	if err := s.store.SaveSecret(job.ID, StoredSecret{
		PostgresUser:       defaultPostgresSuperuser,
		PostgresPassword:   secrets.PostgresPassword,
		ReplicatorUser:     defaultReplicationUser,
		ReplicatorPassword: secrets.ReplicatorPassword,
		AdminPassword:      secrets.AdminPassword,
	}); err != nil {
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
	if job.Status == JobStatusRunning {
		return nil, fmt.Errorf("job %s is already running", jobID)
	}

	startIndex := job.LastCompletedStep + 1
	if startIndex >= len(s.steps) {
		job.Status = JobStatusCompleted
		job.Error = ""
		_ = s.store.Save(job)
		return job, nil
	}
	if job.Request.NewUser != "" && secret.NewUserPassword == "" {
		return nil, fmt.Errorf("new_user_password is required to resume job %s", jobID)
	}
	if secret.PostgresPassword == "" || secret.ReplicatorPassword == "" || secret.AdminPassword == "" {
		storedSecret, err := s.store.LoadSecret(job.ID)
		if err == nil {
			if secret.PostgresPassword == "" {
				secret.PostgresPassword = storedSecret.PostgresPassword
			}
			if secret.ReplicatorPassword == "" {
				secret.ReplicatorPassword = storedSecret.ReplicatorPassword
			}
			if secret.AdminPassword == "" {
				secret.AdminPassword = storedSecret.AdminPassword
			}
		}
	}
	if secret.PostgresPassword == "" {
		secret.PostgresPassword = stringOrGenerated("")
	}
	if secret.ReplicatorPassword == "" {
		secret.ReplicatorPassword = stringOrGenerated("")
	}
	if secret.AdminPassword == "" {
		secret.AdminPassword = stringOrGenerated("")
	}
	if err := s.store.SaveSecret(job.ID, StoredSecret{
		PostgresUser:       defaultPostgresSuperuser,
		PostgresPassword:   secret.PostgresPassword,
		ReplicatorUser:     defaultReplicationUser,
		ReplicatorPassword: secret.ReplicatorPassword,
		AdminPassword:      secret.AdminPassword,
	}); err != nil {
		return nil, err
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
	secret := SecretInput{
		PostgresPassword:   storedSecret.PostgresPassword,
		ReplicatorPassword: storedSecret.ReplicatorPassword,
		AdminPassword:      storedSecret.AdminPassword,
	}
	timeout := time.Duration(deployJob.Request.StepTimeoutSeconds) * time.Second
	newIPs := memberJob.MemberOp.MemberIPs

	// Snapshot the current spec so all goroutines share the same read-only base.
	baseSpec := deployJob.Request
	baseSpec.StandbyIPs = append([]string{}, deployJob.Request.StandbyIPs...)

	// Mark all nodes as in-flight so the caller can see what's running.
	memberJob.CurrentStep = strings.Join(newIPs, ",")
	s.updateJobProgress(memberJob)
	_ = s.store.Save(memberJob)

	// Run each member addition in parallel — their Ansible runs are independent
	// (separate temp dirs, inventories, and pg_basebackup streams from primary).
	results := make([]StepResult, len(newIPs))
	var wg sync.WaitGroup
	for i, ip := range newIPs {
		i, ip := i, ip
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = s.doAddMember(ctx, memberRunConfig{
				jobID:    deployJob.ID,
				spec:     baseSpec,
				secret:   secret,
				memberIP: ip,
				timeout:  timeout,
			})
		}()
	}
	wg.Wait()

	// Collect results; add successful nodes to the deploy job's standby list.
	var failed, added []string
	for i, result := range results {
		memberJob.Steps = append(memberJob.Steps, result)
		ip := newIPs[i]
		if result.Status == JobStatusCompleted {
			added = append(added, ip)
		} else {
			msg := result.Message
			if msg == "" {
				msg = fmt.Sprintf("add member %s failed", ip)
			}
			failed = append(failed, msg)
		}
	}
	// Re-read and persist the deploy job under the store lock so a concurrent
	// member operation on the same cluster cannot clobber the standby list.
	_ = s.store.Update(deployJob.ID, func(j *Job) error {
		j.Request.StandbyIPs = append(j.Request.StandbyIPs, added...)
		deployJob.Request.StandbyIPs = j.Request.StandbyIPs
		return nil
	})
	memberJob.Request.StandbyIPs = deployJob.Request.StandbyIPs

	memberJob.CurrentStep = ""
	if len(failed) > 0 {
		memberJob.Status = JobStatusFailed
		memberJob.Error = strings.Join(failed, "; ")
		s.updateJobProgress(memberJob)
		_ = s.store.Save(memberJob)
		return
	}

	memberJob.Status = JobStatusCompleted
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
	secret := SecretInput{
		PostgresPassword:   storedSecret.PostgresPassword,
		ReplicatorPassword: storedSecret.ReplicatorPassword,
		AdminPassword:      storedSecret.AdminPassword,
	}
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

func (s *Service) CollectMetrics(ctx context.Context, req MetricRequest) MetricResponse {
	return s.collector.Collect(ctx, req)
}

func (s *Service) ConnectionInfo(ctx context.Context, jobID string) (host string, port int, user, password string, nodeIPs []string, err error) {
	job, err := s.store.Load(jobID)
	if err != nil {
		return "", 0, "", "", nil, fmt.Errorf("load job %q: %w", jobID, err)
	}
	secret, err := s.store.LoadSecret(jobID)
	if err != nil {
		return "", 0, "", "", nil, fmt.Errorf("load job secret %q: %w", jobID, err)
	}
	p := job.Request.PostgresPort
	if p == 0 {
		p = 5432
	}
	ips := append([]string{job.Request.PrimaryIP}, job.Request.StandbyIPs...)
	return job.Request.PrimaryIP, p, secret.PostgresUser, secret.PostgresPassword, ips, nil
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

func shouldSkipStep(st step, spec StoredSpec) (string, bool) {
	if st.Name == "standby_config" && len(spec.StandbyIPs) == 0 {
		return "standby_ips is empty", true
	}
	if st.Skippable && (spec.NewUser == "" || spec.NewDB == "") {
		return "new_user/new_db not provided", true
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
