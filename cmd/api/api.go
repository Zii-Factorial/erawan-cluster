package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	mysqlcluster "erawan-cluster/internal/cluster/mysql"
	pgsqlcluster "erawan-cluster/internal/cluster/pgsql"
	"erawan-cluster/internal/haproxy"
	"erawan-cluster/internal/security"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

type application struct {
	config       config
	haproxy      *haproxy.Service
	mysqlCluster *mysqlcluster.Service
	pgsqlCluster *pgsqlcluster.Service
	baseDir      string
}

type config struct {
	addr    string
	env     string
	apiKey  string
	version string
}

func (app *application) mount() *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Minute))
	r.Use(security.APIKeyMiddleware(app.config.apiKey))
	r.Use(bodyLimit(1 << 20))

	r.Get("/health", app.healthCheckHandler)
	r.Get("/docs", app.docsHandler)

	r.Route("/haproxy", func(r chi.Router) {
		r.Post("/config", app.createHAProxyConfigHandler)
		r.Delete("/config", app.deleteHAProxyConfigHandler)
		r.Get("/configs", app.listHAProxyConfigsHandler)
		r.Post("/reload", app.reloadHAProxyHandler)
	})

	r.Route("/cluster/mysql", func(r chi.Router) {
		r.Post("/deploy", app.deployMySQLClusterHandler)
		r.Get("/jobs", app.listMySQLClusterJobsHandler)
		r.Get("/jobs/{jobID}", app.getMySQLClusterJobHandler)
		r.Post("/jobs/{jobID}/resume", app.resumeMySQLClusterJobHandler)
		r.Post("/jobs/{jobID}/rollback", app.rollbackMySQLClusterJobHandler)
	})

	r.Route("/cluster/pgsql", func(r chi.Router) {
		r.Post("/deploy", app.deployPGSQLClusterHandler)
		r.Get("/jobs", app.listPGSQLClusterJobsHandler)
		r.Get("/jobs/{jobID}", app.getPGSQLClusterJobHandler)
		r.Post("/jobs/{jobID}/resume", app.resumePGSQLClusterJobHandler)
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
