# Adding a new database engine

erawan-cluster is structured so that a new engine (MariaDB, MongoDB, Redis, …)
is a **thin adapter** over the shared, engine-agnostic machinery in
[`internal/cluster/core`](../internal/cluster/core), instead of a copy of an
existing engine package.

## What `core` already gives you

| Concern | Provided by `core` | You reuse via |
|---|---|---|
| Job state types | `core.Job[Spec]`, `core.StepResult`, `core.MemberOperation`, `core.Step`, status constants | `type Job = core.Job[StoredSpec]` aliases |
| Job/secret persistence | `core.Store[Spec, Sec]` (`Save`/`Load`/`List`/`SaveSecret`/`LoadSecret`/`Update`/`MarkStaleRunningJobsFailed`) | `type Store = core.Store[StoredSpec, StoredSecret]` |
| Ansible execution | `core.AnsibleRun(ctx, core.AnsibleSpec{...})` — temp workspace, `vars.json` (0600), argv, capture, timeout, result mapping | call it from your `Runner` |
| Concurrency + shutdown | `core.Launcher` (bounded background jobs, `Wait` to drain) | wired by `Service` automatically |
| Progress accounting | `core.ApplyProgress`, `core.CountCompletedSteps` | call from `updateJobProgress` |
| SSH host-key policy | `core.SSHPolicy` (secure default) | `Runner.SetSSHPolicy` |
| ID / secret helpers | `core.NewJobID`, `core.OrRandomSecret` | — |

## Steps to add engine `foo`

1. **Create `internal/cluster/foo/`** with:
   - `types.go` — engine-specific `StoredSpec`, `StoredSecret`, `DeployRequest`,
     `ResumeRequest`, member requests, plus the aliases
     `type Job = core.Job[StoredSpec]`, `type StepResult = core.StepResult`,
     `type MemberOperation = core.MemberOperation`, and status constants
     re-exported from `core`.
   - `store.go` — `type Store = core.Store[StoredSpec, StoredSecret]` and a
     `NewStore(dir)` wrapper.
   - `validate.go` — request validation (reuse the regexp patterns / `net.ParseIP`
     style from `mysql/validate.go`).
   - `runner.go` — build the engine's inventory YAML and extra-vars map, then call
     `core.AnsibleRun`. Pass `r.sshPolicy.SSHCommonArgs()` into the inventory and
     `r.sshPolicy.AnsibleEnv()` as the run env. Expose `SetSSHPolicy`.
   - `service.go` — the deploy step list, `shouldSkipStep`, and the
     orchestration (`Deploy`/`Resume`/member ops) using the `core` store +
     launcher + progress helpers. Copy the shape from `mysql/service.go`.
   - `metric_collector.go` / `metric_types.go` — engine-specific metric SQL.
   - `dbmanager/` — user/database management (reuse the identifier-quoting and
     TLS-mode patterns; see [security.md](security.md)).
2. **Add the playbooks** under `cluster/foo/playbooks/` (deploy, add_member,
   remove_member, optional rollback) with the step tags your `Service` lists.
3. **Wire configuration** in [`cmd/api/config.go`](../cmd/api/config.go): the
   generic `loadClusterEngineConfig("foo", …)` already derives state dir and
   playbook paths and the `FOO_*` env overrides — just add a `foo` field to
   `runtimeConfig` and populate it in `loadConfig`.
4. **Assemble** in [`cmd/api/setup.go`](../cmd/api/setup.go): add a
   `buildFooCluster` mirroring `buildMySQLCluster` (it already applies
   `SetMaxConcurrentJobs` and `SetSSHPolicy`), and attach the service in
   `buildApplication`.
5. **Mount routes** in [`cmd/api/api.go`](../cmd/api/api.go) and add a handler
   package under `cmd/api/foo/` mirroring `cmd/api/mysql/`.

## What you must NOT reimplement

The temp-dir/`vars.json`/exec/capture loop, the file store, the concurrency
cap, progress math, the capped output buffer, and the SSH host-key decision all
live in `core`. If you find yourself copying those from `mysql`/`pgsql`, promote
the shared part to `core` instead.
