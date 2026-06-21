package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	mysqlcluster "erawan-cluster/internal/cluster/mysql"
	mysqldbmanager "erawan-cluster/internal/cluster/mysql/dbmanager"
	pgsqlcluster "erawan-cluster/internal/cluster/pgsql"
	"erawan-cluster/internal/cluster/pgsql/dbmanager"
	"erawan-cluster/internal/haproxy"
	"erawan-cluster/internal/security"

	haproxyapi "erawan-cluster/cmd/api/haproxy"
	mysqlapi "erawan-cluster/cmd/api/mysql"
	pgsqlapi "erawan-cluster/cmd/api/pgsql"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

type application struct {
	config       config
	haproxy      *haproxy.Service
	mysqlCluster *mysqlcluster.Service
	pgsqlCluster *pgsqlcluster.Service
	pgsqlDB      *dbmanager.Service
	mysqlDB      *mysqldbmanager.Service
	cipher       *security.Cipher
	baseDir      string
}

type config struct {
	addr      string
	env       string
	apiKey    string
	version   string
	proxyHost string // HAProxy host for metric connections; from PROXY_HOST env (default 127.0.0.1)
}

func (app *application) mount() *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Minute))
	r.Use(security.APIKeyMiddleware(app.config.apiKey))
	r.Use(security.EncryptMiddleware(app.cipher))
	r.Use(bodyLimit(2 << 20)) // 2 MB covers ~1.5 MB plaintext after AES-GCM base64 overhead
	r.Use(security.DecryptMiddleware(app.cipher))

	r.Get("/health", app.healthCheckHandler)
	r.Get("/docs", app.docsHandler)

	haproxyH := haproxyapi.New(app.haproxy)
	r.Route("/haproxy", func(r chi.Router) {
		r.Post("/config/mysql", haproxyH.CreateMySQLConfig)
		r.Patch("/config/mysql", haproxyH.AddMySQLMember)
		r.Post("/config/pgsql", haproxyH.CreatePGSQLConfig)
		r.Patch("/config/pgsql", haproxyH.AddPGSQLMember)
		r.Delete("/config", haproxyH.DeleteConfig)
		r.Get("/configs", haproxyH.ListConfigs)
		r.Get("/configs/download", haproxyH.DownloadZip)
		r.Post("/reload", haproxyH.Reload)
	})

	mysqlH := mysqlapi.New(app.mysqlCluster, app.mysqlDB, app.config.proxyHost)
	r.Route("/cluster/mysql", func(r chi.Router) {
		r.Post("/deploy", mysqlH.Deploy)
		r.Post("/metrics", mysqlH.Metrics)
		r.Get("/jobs", mysqlH.ListJobs)
		r.Get("/jobs/{jobID}", mysqlH.GetJob)
		r.Post("/jobs/{jobID}/resume", mysqlH.ResumeJob)
		r.Post("/jobs/{jobID}/rollback", mysqlH.RollbackJob)
		r.Post("/members", mysqlH.AddMember)
		r.Delete("/members", mysqlH.RemoveMember)
		r.Post("/users", mysqlH.CreateUser)
		r.Patch("/users", mysqlH.UpdateUser)
		r.Put("/users/password", mysqlH.ResetPassword)
		r.Delete("/users", mysqlH.DeleteUser)
		r.Post("/databases", mysqlH.CreateDatabase)
		r.Patch("/databases", mysqlH.UpdateDatabase)
		r.Delete("/databases", mysqlH.DeleteDatabase)
	})

	pgsqlH := pgsqlapi.New(app.pgsqlCluster, app.pgsqlDB, app.config.proxyHost)
	r.Route("/cluster/pgsql", func(r chi.Router) {
		r.Post("/deploy", pgsqlH.Deploy)
		r.Post("/metrics", pgsqlH.Metrics)
		r.Get("/jobs", pgsqlH.ListJobs)
		r.Get("/jobs/{jobID}", pgsqlH.GetJob)
		r.Post("/jobs/{jobID}/resume", pgsqlH.ResumeJob)
		r.Post("/members", pgsqlH.AddMember)
		r.Delete("/members", pgsqlH.RemoveMember)
		r.Post("/users", pgsqlH.CreateUser)
		r.Patch("/users", pgsqlH.UpdateUser)
		r.Put("/users/password", pgsqlH.ResetPassword)
		r.Delete("/users", pgsqlH.DeleteUser)
		r.Post("/databases", pgsqlH.CreateDatabase)
		r.Patch("/databases", pgsqlH.UpdateDatabase)
		r.Delete("/databases", pgsqlH.DeleteDatabase)
	})

	return r
}

func (app *application) run(ctx context.Context, mux *chi.Mux) error {
	srv := &http.Server{
		Addr:         app.config.addr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Minute,
		IdleTimeout:  time.Minute,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func bodyLimit(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			next.ServeHTTP(w, r)
		})
	}
}

func (app *application) docsHandler(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, filepath.Join(app.baseDir, "index.html"))
}

func projectBaseDir() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}
