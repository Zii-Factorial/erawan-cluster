package pgsql

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
	store            Store
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

/**
 * NewService.
 *
 * Params:
 *   store *Store - the store (*Store)
 *   runner *Runner - the runner (*Runner)
 *
 * Returns:
 *   *Service - the resulting *Service
 */
func NewService(store Store, runner *Runner) *Service {
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
			{Name: "setup_exporters", Tag: "setup_exporters"},
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

/**
 * SetContext.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 */
func (s *Service) SetContext(ctx context.Context) {
	s.ctx = ctx
}

/**
 * SetMaxConcurrentJobs bounds how many background jobs run at once. It must be
 * called at start-up, before any job is launched.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   n int - the n value
 */
func (s *Service) SetMaxConcurrentJobs(n int) {
	s.launcher = core.NewLauncher(n)
	s.start = s.launcher.Go
}

/**
 * Wait blocks until all in-flight background jobs finish or ctx is done. Used
 * during graceful shutdown to drain running jobs.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 */
func (s *Service) Wait(ctx context.Context) {
	s.launcher.Wait(ctx)
}

/**
 * SetSSHConfig.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   user string - the user string
 *   privateKeyPath string - the privateKeyPath string
 *
 * Returns:
 *   error - error value; non-nil when the operation fails
 */
func (s *Service) SetSSHConfig(user, privateKeyPath string) error {
	normalizedUser, normalizedKeyPath, err := ValidateServiceSSHConfig(user, privateKeyPath)
	if err != nil {
		return err
	}
	s.sshUser = normalizedUser
	s.sshKeyPath = normalizedKeyPath
	return nil
}

/**
 * Deploy.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   req DeployRequest - the req (DeployRequest)
 *
 * Returns:
 *   *Job - the resulting *Job
 *   error - error value; non-nil when the operation fails
 */
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
			NewUserSuperuser:   req.NewUserSuperuserEnabled(),
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
		ExporterPassword:   stringOrGenerated(""),
	}
	if err := s.store.SaveSecret(job.ID, StoredSecret{
		PostgresUser:       defaultPostgresSuperuser,
		PostgresPassword:   secrets.PostgresPassword,
		ReplicatorUser:     defaultReplicationUser,
		ReplicatorPassword: secrets.ReplicatorPassword,
		AdminPassword:      secrets.AdminPassword,
		ExporterPassword:   secrets.ExporterPassword,
	}); err != nil {
		return nil, err
	}

	bgJob, err := s.store.Load(job.ID)
	if err != nil {
		return nil, err
	}
	resetHostKeys := req.ResetHostKeys
	s.start(func() {
		_ = s.executeFrom(s.ctx, bgJob, 0, secrets, resetHostKeys)
	})
	return job, nil
}

/**
 * Resume.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   jobID string - the jobID string
 *   req ResumeRequest - the req (ResumeRequest)
 *
 * Returns:
 *   *Job - the resulting *Job
 *   error - error value; non-nil when the operation fails
 */
func (s *Service) Resume(ctx context.Context, jobID string, req ResumeRequest) (*Job, error) {
	_ = ctx
	secret, err := ValidateResumeSecrets(req)
	if err != nil {
		return nil, err
	}

	// Atomically validate status and transition to Running so concurrent
	// Resume calls (e.g. during a brief VIP overlap) cannot both win.
	var job *Job
	var startIndex int
	if err := s.store.Update(jobID, func(j *Job) error {
		switch j.Status {
		case JobStatusCompleted:
			return fmt.Errorf("job %s already completed; use the recover endpoint to restart the cluster after an outage", jobID)
		case JobStatusRunning:
			return fmt.Errorf("job %s is already running", jobID)
		}
		if err := s.hydrateStoredSSHConfig(j); err != nil {
			return err
		}
		startIndex = j.LastCompletedStep + 1
		if startIndex >= len(s.steps) {
			j.Status = JobStatusCompleted
			j.Error = ""
			job = j
			return nil
		}
		if j.Request.NewUser != "" && secret.NewUserPassword == "" {
			return fmt.Errorf("new_user_password is required to resume job %s", jobID)
		}
		j.Status = JobStatusRunning
		j.Error = ""
		s.updateJobProgress(j)
		job = j
		return nil
	}); err != nil {
		return nil, err
	}

	if job.Status == JobStatusCompleted {
		return job, nil
	}

	if secret.PostgresPassword == "" || secret.ReplicatorPassword == "" || secret.AdminPassword == "" || secret.ExporterPassword == "" {
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
			if secret.ExporterPassword == "" {
				secret.ExporterPassword = storedSecret.ExporterPassword
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
	if secret.ExporterPassword == "" {
		secret.ExporterPassword = stringOrGenerated("")
	}
	if err := s.store.SaveSecret(job.ID, StoredSecret{
		PostgresUser:       defaultPostgresSuperuser,
		PostgresPassword:   secret.PostgresPassword,
		ReplicatorUser:     defaultReplicationUser,
		ReplicatorPassword: secret.ReplicatorPassword,
		AdminPassword:      secret.AdminPassword,
		ExporterPassword:   secret.ExporterPassword,
	}); err != nil {
		return nil, err
	}

	bgJob, err := s.store.Load(job.ID)
	if err != nil {
		return nil, err
	}
	resetHostKeys := req.ResetHostKeys
	s.start(func() {
		_ = s.executeFrom(s.ctx, bgJob, startIndex, secret, resetHostKeys)
	})
	return job, nil
}

/**
 * Recover launches a new recovery job against the cluster owned by jobID, running
 * cluster_bootstrap and verify_cluster. Use after a complete datacenter outage:
 * this re-registers the cluster in the DCS (etcd/consul) and restarts Patroni on
 * all nodes without touching data directories. Stored secrets are used so no
 * passwords are required at call time.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   jobID string - ID of the original completed or failed deploy job
 *
 * Returns:
 *   *Job - the new running recovery job
 *   error - error value; non-nil when the operation fails
 */
func (s *Service) Recover(ctx context.Context, jobID string) (*Job, error) {
	_ = ctx
	deployJob, err := s.store.Load(jobID)
	if err != nil {
		return nil, err
	}
	if err := s.hydrateStoredSSHConfig(deployJob); err != nil {
		return nil, err
	}
	if deployJob.Status == JobStatusRunning {
		return nil, fmt.Errorf("job %s is currently running; wait for it to finish before recovering", jobID)
	}
	if deployJob.Status == core.JobStatusRolledBack {
		return nil, fmt.Errorf("job %s was rolled back; run a new deploy instead of recovering", jobID)
	}

	storedSecret, err := s.store.LoadSecret(jobID)
	if err != nil {
		return nil, fmt.Errorf("load job secret: %w", err)
	}
	secret := SecretInput{
		PostgresPassword:   storedSecret.PostgresPassword,
		ReplicatorPassword: storedSecret.ReplicatorPassword,
		AdminPassword:      storedSecret.AdminPassword,
		ExporterPassword:   storedSecret.ExporterPassword,
	}

	recoverySteps := s.recoveryStepsFor()
	recoveryJob := &Job{
		ID:                newJobID(),
		Status:            JobStatusRunning,
		CreatedAt:         time.Now().UTC(),
		UpdatedAt:         time.Now().UTC(),
		LastCompletedStep: -1,
		Request:           deployJob.Request,
		RecoveryOp:        &core.RecoveryOperation{SourceJobID: jobID},
		Steps:             make([]StepResult, 0, len(recoverySteps)),
	}
	s.updateJobProgress(recoveryJob)
	if err := s.store.Save(recoveryJob); err != nil {
		return nil, err
	}

	bgJob, err := s.store.Load(recoveryJob.ID)
	if err != nil {
		return nil, err
	}
	bgDeployJob, err := s.store.Load(jobID)
	if err != nil {
		return nil, err
	}
	s.start(func() {
		s.executeRecovery(s.ctx, bgJob, bgDeployJob, secret)
	})
	return recoveryJob, nil
}

// recoveryStepsFor returns the ordered Ansible steps for PostgreSQL post-outage
// recovery: cluster_bootstrap re-registers in DCS and starts Patroni; verify_cluster
// confirms the cluster is healthy. Both are safe to re-run on existing data.
func (s *Service) recoveryStepsFor() []step {
	return []step{
		{Name: "cluster_bootstrap", Tag: "cluster_bootstrap"},
		{Name: "verify_cluster", Tag: "verify_cluster"},
	}
}

/**
 * executeRecovery runs the recovery steps for a post-outage recovery job.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   recoveryJob *Job - the new recovery job being tracked
 *   deployJob *Job - the original deploy job supplying the cluster configuration
 *   secret SecretInput - the credentials to pass to Ansible
 */
func (s *Service) executeRecovery(ctx context.Context, recoveryJob *Job, deployJob *Job, secret SecretInput) {
	timeout := time.Duration(deployJob.Request.StepTimeoutSeconds) * time.Second
	recoverySteps := s.recoveryStepsFor()

	for i, st := range recoverySteps {
		recoveryJob.CurrentStep = st.Name
		s.updateJobProgress(recoveryJob)
		_ = s.store.Save(recoveryJob)

		res := s.runDeploy(ctx, runConfig{
			jobID:   deployJob.ID,
			spec:    deployJob.Request,
			secret:  secret,
			step:    st,
			timeout: timeout,
		})
		recoveryJob.Steps = append(recoveryJob.Steps, res)

		if res.Status != JobStatusCompleted {
			recoveryJob.Status = JobStatusFailed
			recoveryJob.Error = res.Message
			if recoveryJob.Error == "" {
				recoveryJob.Error = fmt.Sprintf("recovery step %s failed", st.Name)
			}
			recoveryJob.LastCompletedStep = i - 1
			recoveryJob.CurrentStep = ""
			s.updateJobProgress(recoveryJob)
			_ = s.store.Save(recoveryJob)
			return
		}
		recoveryJob.LastCompletedStep = i
		recoveryJob.Error = ""
	}

	recoveryJob.Status = JobStatusCompleted
	recoveryJob.CurrentStep = ""
	recoveryJob.Error = ""
	s.updateJobProgress(recoveryJob)
	_ = s.store.Save(recoveryJob)
}

/**
 * claimMemberOpLock atomically marks deployJobID as owned by memberJobID for
 * the duration of a member operation. Two add/remove-member calls racing
 * against the same cluster both mutate etcd/Patroni membership (e.g.
 * overlapping learner promotions), which can transiently break quorum on the
 * primary — see core.Job.ActiveMemberJobID. Rejecting the second caller up
 * front surfaces a clear error immediately instead of letting it fail deep
 * inside Ansible a minute later.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   deployJobID string - the deploy job whose cluster is being claimed
 *   memberJobID string - the member job claiming it
 *
 * Returns:
 *   error - non-nil if the deploy job is still running or another member
 *     operation already holds the claim
 */
func (s *Service) claimMemberOpLock(deployJobID, memberJobID string) error {
	return s.store.Update(deployJobID, func(j *Job) error {
		if j.Status == JobStatusRunning {
			return fmt.Errorf("deploy job %s is still running; wait for it to finish before starting a member operation", deployJobID)
		}
		if j.ActiveMemberJobID != "" {
			return fmt.Errorf("another member operation (%s) is already running against job %s; wait for it to finish first", j.ActiveMemberJobID, deployJobID)
		}
		j.ActiveMemberJobID = memberJobID
		return nil
	})
}

/**
 * releaseMemberOpLock clears deployJobID's claim if it is still held by
 * memberJobID. Safe to call multiple times or after a failed claim.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   deployJobID string - the deploy job to release
 *   memberJobID string - the member job releasing it
 */
func (s *Service) releaseMemberOpLock(deployJobID, memberJobID string) {
	_ = s.store.Update(deployJobID, func(j *Job) error {
		if j.ActiveMemberJobID == memberJobID {
			j.ActiveMemberJobID = ""
		}
		return nil
	})
}

/**
 * AddMember.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   req AddMemberRequest - the req (AddMemberRequest)
 *
 * Returns:
 *   *Job - the resulting *Job
 *   error - error value; non-nil when the operation fails
 */
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

	memberJobID := newJobID()
	if err := s.claimMemberOpLock(req.JobID, memberJobID); err != nil {
		return nil, err
	}

	memberJob := &Job{
		ID:                memberJobID,
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
		s.releaseMemberOpLock(req.JobID, memberJobID)
		return nil, err
	}

	bgMemberJob, err := s.store.Load(memberJob.ID)
	if err != nil {
		s.releaseMemberOpLock(req.JobID, memberJobID)
		return nil, err
	}
	bgDeployJob, err := s.store.Load(req.JobID)
	if err != nil {
		s.releaseMemberOpLock(req.JobID, memberJobID)
		return nil, err
	}
	resetHostKeys := req.ResetHostKeys
	s.start(func() {
		defer s.releaseMemberOpLock(req.JobID, memberJobID)
		s.executeMemberAdd(s.ctx, bgMemberJob, bgDeployJob, resetHostKeys)
	})
	return memberJob, nil
}

/**
 * RemoveMember.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   req RemoveMemberRequest - the req (RemoveMemberRequest)
 *
 * Returns:
 *   *Job - the resulting *Job
 *   error - error value; non-nil when the operation fails
 */
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

	memberJobID := newJobID()
	if err := s.claimMemberOpLock(req.JobID, memberJobID); err != nil {
		return nil, err
	}

	memberJob := &Job{
		ID:                memberJobID,
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
		s.releaseMemberOpLock(req.JobID, memberJobID)
		return nil, err
	}

	bgMemberJob, err := s.store.Load(memberJob.ID)
	if err != nil {
		s.releaseMemberOpLock(req.JobID, memberJobID)
		return nil, err
	}
	bgDeployJob, err := s.store.Load(req.JobID)
	if err != nil {
		s.releaseMemberOpLock(req.JobID, memberJobID)
		return nil, err
	}
	force := req.Force
	s.start(func() {
		defer s.releaseMemberOpLock(req.JobID, memberJobID)
		s.executeMemberRemove(s.ctx, bgMemberJob, bgDeployJob, force)
	})
	return memberJob, nil
}

/**
 * executeMemberAdd.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   memberJob *Job - the memberJob (*Job)
 *   deployJob *Job - the deployJob (*Job)
 *   resetHostKeys bool - when true, forget any pinned SSH host key for the
 *     new members before connecting
 */
func (s *Service) executeMemberAdd(ctx context.Context, memberJob *Job, deployJob *Job, resetHostKeys bool) {
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

	// Members are added strictly one at a time: every addition mutates etcd
	// membership on the primary (etcd allows a single unpromoted learner by
	// default), and each run's register play treats members outside its
	// standby list as stale registrations to clean up — so a sibling still
	// mid-join would be seen as removable. Appending each success to
	// StandbyIPs before the next run makes it a legitimate member for the
	// runs that follow.
	for _, ip := range memberJob.MemberOp.MemberIPs {
		memberJob.CurrentStep = ip
		s.updateJobProgress(memberJob)
		_ = s.store.Save(memberJob)

		result := s.doAddMember(ctx, memberRunConfig{
			jobID:         deployJob.ID,
			spec:          deployJob.Request,
			secret:        secret,
			memberIP:      ip,
			timeout:       timeout,
			resetHostKeys: resetHostKeys,
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

/**
 * executeMemberRemove.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   memberJob *Job - the memberJob (*Job)
 *   deployJob *Job - the deployJob (*Job)
 *   force bool - the force flag
 */
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

/**
 * doAddMember.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   cfg memberRunConfig - the cfg (memberRunConfig)
 *
 * Returns:
 *   StepResult - the resulting StepResult
 */
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

/**
 * doRemoveMember.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   cfg memberRunConfig - the cfg (memberRunConfig)
 *
 * Returns:
 *   StepResult - the resulting StepResult
 */
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

/**
 * Get.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   jobID string - the jobID string
 *
 * Returns:
 *   *Job - the resulting *Job
 *   error - error value; non-nil when the operation fails
 */
func (s *Service) Get(jobID string) (*Job, error) {
	job, err := s.store.Load(jobID)
	if err != nil {
		return nil, err
	}
	s.updateJobProgress(job)
	return job, nil
}

/**
 * GetSecret.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   jobID string - the jobID string
 *
 * Returns:
 *   *StoredSecret - the resulting *StoredSecret
 *   error - error value; non-nil when the operation fails
 */
func (s *Service) GetSecret(jobID string) (*StoredSecret, error) {
	secret, err := s.store.LoadSecret(jobID)
	if err != nil {
		return nil, err
	}
	return &secret, nil
}

/**
 * List.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   limit int - the limit value
 *
 * Returns:
 *   []Job - the resulting []Job
 *   error - error value; non-nil when the operation fails
 */
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

/**
 * hydrateStoredSSHConfig.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   job *Job - the job (*Job)
 *
 * Returns:
 *   error - error value; non-nil when the operation fails
 */
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

/**
 * executeFrom.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   job *Job - the job (*Job)
 *   startIndex int - the startIndex value
 *   secret SecretInput - the secret (SecretInput)
 *   resetHostKeys bool - when true, forget any pinned SSH host key for this
 *     cluster's nodes before connecting, so a rebuilt/reimaged node's new key
 *     is trusted instead of failing host-key verification
 *
 * Returns:
 *   error - error value; non-nil when the operation fails
 */
func (s *Service) executeFrom(ctx context.Context, job *Job, startIndex int, secret SecretInput, resetHostKeys bool) error {
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
			jobID:         job.ID,
			spec:          job.Request,
			secret:        secret,
			step:          st,
			timeout:       timeout,
			resetHostKeys: resetHostKeys,
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

/**
 * runDeploy.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   cfg runConfig - the cfg (runConfig)
 *
 * Returns:
 *   StepResult - the resulting StepResult
 */
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

/**
 * CollectMetrics.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   req MetricRequest - the req (MetricRequest)
 *
 * Returns:
 *   MetricResponse - the resulting MetricResponse
 */
func (s *Service) CollectMetrics(ctx context.Context, req MetricRequest) MetricResponse {
	return s.collector.Collect(ctx, req)
}

/**
 * ConnectionInfo.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 *   jobID string - the jobID string
 *
 * Returns:
 *   host string - the host string
 *   port int - the port value
 *   user string - the user string
 *   password string - the password string
 *   nodeIPs []string - the nodeIPs ([]string)
 *   err error - error value; non-nil when the operation fails
 */
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

/**
 * updateJobProgress.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   job *Job - the job (*Job)
 */
func (s *Service) updateJobProgress(job *Job) {
	total := s.totalStepsFor(job.Request)
	if job.MemberOp != nil {
		total = len(job.MemberOp.MemberIPs)
	}
	if job.RecoveryOp != nil {
		total = len(s.recoveryStepsFor())
	}
	core.ApplyProgress(job, total)
}

/**
 * totalStepsFor.
 *
 * Receiver:
 *   s *Service - pointer receiver; the method may mutate this Service instance
 *
 * Params:
 *   spec StoredSpec - the spec (StoredSpec)
 *
 * Returns:
 *   int - the resulting integer
 */
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

/**
 * shouldSkipStep.
 *
 * Params:
 *   st step - the st (step)
 *   spec StoredSpec - the spec (StoredSpec)
 *
 * Returns:
 *   string - the resulting string
 *   bool - boolean result
 */
func shouldSkipStep(st step, spec StoredSpec) (string, bool) {
	if st.Name == "standby_config" && len(spec.StandbyIPs) == 0 {
		return "standby_ips is empty", true
	}
	if st.Skippable && (spec.NewUser == "" || spec.NewDB == "") {
		return "new_user/new_db not provided", true
	}
	return "", false
}

/**
 * without returns slice with every occurrence of target removed.
 *
 * Params:
 *   slice []string - the slice ([]string)
 *   target string - the target string
 *
 * Returns:
 *   []string - the resulting []string
 */
func without(slice []string, target string) []string {
	out := make([]string, 0, len(slice))
	for _, v := range slice {
		if v != target {
			out = append(out, v)
		}
	}
	return out
}

/**
 * newJobID.
 *
 * Returns:
 *   string - the resulting string
 */
func newJobID() string { return core.NewJobID() }

/**
 * stringOrGenerated.
 *
 * Params:
 *   value string - the value string
 *
 * Returns:
 *   string - the resulting string
 */
func stringOrGenerated(value string) string { return core.OrRandomSecret(value) }
