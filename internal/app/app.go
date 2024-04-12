package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"
	ssogrpc "url_shortener/internal/clients/sso/grpc"
	"url_shortener/internal/config"
	"url_shortener/internal/lib/logger/sl"
	"url_shortener/internal/storage/postgres"
	admDel "url_shortener/internal/transport/handlers/admins/delete"
	admSet "url_shortener/internal/transport/handlers/admins/set"
	urlDel "url_shortener/internal/transport/handlers/url/delete"
	urlRed "url_shortener/internal/transport/handlers/url/redirect"
	urlSave "url_shortener/internal/transport/handlers/url/save"
	"url_shortener/internal/transport/middleware/auth"
	mwLogger "url_shortener/internal/transport/middleware/logger"

	"github.com/go-chi/chi"
	"github.com/go-chi/chi/middleware"
)

func RunServer(ctx context.Context, log *slog.Logger, cfg *config.Config) error {
	const op = "internal.app.RunServer"
	log.With(slog.String("op", op))

	// init ssoServer
	log.Info("init ssoServer", slog.String("env", cfg.Env))
	log.Debug("creddentials sso", slog.String("address", cfg.Clients.SSO.Address))
	ssoClient, err := ssogrpc.New(
		context.Background(),
		log, cfg.Clients.SSO.Address,
		cfg.Clients.SSO.Timeout,
		cfg.Clients.SSO.RetriesCount,
	)
	if err != nil {
		log.Error("failed to init ssoClient", sl.Err(err))
		return fmt.Errorf("%s: %w", op, err)
	}
	log.Info("ssoClient was init")

	// init postgresql storage
	storage, err := postgres.NewStorage(cfg)
	if err != nil {
		log.Error("failed to init storage", sl.Err(err))
		return fmt.Errorf("%s: %w", op, err)
	}
	defer storage.CloseStorage()

	// init router
	router := chi.NewRouter()
	router.Use(middleware.Logger)
	router.Use(mwLogger.New(log))
	router.Use(middleware.RequestID)
	router.Use(middleware.Recoverer)
	router.Use(middleware.URLFormat)

	// url router
	router.Route("/url", func(r chi.Router) {
		r.Use(auth.New(log, cfg.AppSecret, ssoClient))
		r.Post("/", urlSave.New(log, storage))
		r.Delete("/{alias}", urlDel.New(log, storage))
	})
	router.Get("/{alias}", urlRed.New(log, storage))

	/// user router
	router.Route("/user", func(r chi.Router) {
		r.Use(middleware.BasicAuth("url_shortener", map[string]string{cfg.HTTPServer.User: cfg.HTTPServer.Password}))
		r.Post("/", admSet.New(log, ssoClient))
		r.Delete("/", admDel.New(log, ssoClient))
	})

	// start server
	log.Info("starting server")
	srv := &http.Server{
		Addr:         cfg.Address,
		Handler:      router,
		ReadTimeout:  cfg.HTTPServer.Timeout,
		WriteTimeout: cfg.HTTPServer.Timeout,
		IdleTimeout:  cfg.HTTPServer.IdleTimeout,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("failed to start server")
			os.Exit(1)
		}
	}()
	log.Info("server start", slog.String("addresses", cfg.Address))

	// wait for gracefully shutdown
	<-ctx.Done()
	log.Info("shutting down server gracefully")
	shutDownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutDownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	<-shutDownCtx.Done()
	return nil
}
