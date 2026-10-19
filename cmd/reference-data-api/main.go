package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/XSAM/otelsql"
	"github.com/code-sena/design-software-reference-data-service/internal/adapter/in/http/health"
	"github.com/code-sena/design-software-reference-data-service/internal/adapter/in/http/hierarchy"
	"github.com/code-sena/design-software-reference-data-service/internal/adapter/in/http/security"
	"github.com/code-sena/design-software-reference-data-service/internal/adapter/out/messaging"
	"github.com/code-sena/design-software-reference-data-service/internal/adapter/out/persistence"
	"github.com/code-sena/design-software-reference-data-service/internal/adapter/out/postgres"
	application "github.com/code-sena/design-software-reference-data-service/internal/application/hierarchy"
	"github.com/code-sena/design-software-reference-data-service/internal/platform/observability"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	shutdownTelemetry, err := observability.Setup(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = observability.ShutdownWithTimeout(shutdownTelemetry) }()

	mux := http.NewServeMux()
	healthHandler := health.NewHandler(nil, 2*time.Second)
	mux.HandleFunc("GET /health/live", healthHandler.Live)
	mux.HandleFunc("GET /health", healthHandler.Live)
	// These settings intentionally have no issuer/JWKS defaults: their values are
	// owned by IAM deployment configuration, not by this service's source code.
	validator, err := securityFromEnvironment()
	if err != nil {
		log.Fatal(err)
	}
	db, err := databaseFromEnvironment()
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	healthHandler = health.NewHandler(db, 2*time.Second)
	mux.HandleFunc("GET /health/ready", healthHandler.Ready)
	repository := postgres.NewRepository(db)
	var publisher application.EventPublisher
	var commands *application.CommandService
	if amqpURL := env("REFERENCE_DATA_AMQP_URL", os.Getenv("MESSAGE_BROKER_URL")); amqpURL != "" {
		amqpPublisher := messaging.NewAMQPPublisher(amqpURL)
		publisher = amqpPublisher
		commands = application.NewCommandService(repository, application.WithEventPublisher(publisher), application.WithTransactionalOutbox())
		relay := messaging.NewOutboxRelay(persistence.NewOutboxStore(db), amqpPublisher, 5*time.Second)
		go func() {
			if err := relay.Run(context.Background()); err != nil {
				log.Printf("outbox relay stopped: %v", err)
			}
		}()
	} else {
		publisher = messaging.NewJSONPublisher(os.Stdout)
		commands = application.NewCommandService(repository, application.WithEventPublisher(publisher))
	}
	registerRoutes(mux, validator, hierarchy.NewHandler(application.NewQueryService(repository), commands))
	addr := ":" + env("PORT", "8080")
	log.Printf("api listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, observability.HTTP(security.Correlation(mux))))
}

func securityFromEnvironment() (security.TokenValidator, error) {
	issuer := os.Getenv("IAM_JWT_ISSUER")
	if issuer == "" {
		return nil, fmt.Errorf("IAM_JWT_ISSUER is required")
	}
	jwksURL := os.Getenv("IAM_JWKS_URL")
	if jwksURL == "" {
		return nil, fmt.Errorf("IAM_JWKS_URL is required")
	}
	client, err := security.NewJWKSClient(jwksURL, 5*time.Second, 5*time.Minute)
	if err != nil {
		return nil, err
	}
	return security.NewJWKSValidator(issuer, client)
}

func databaseFromEnvironment() (*sql.DB, error) {
	dsn := os.Getenv("REFERENCE_DATA_DATABASE_URL")
	if dsn == "" {
		return nil, fmt.Errorf("REFERENCE_DATA_DATABASE_URL is required")
	}
	db, err := otelsql.Open("pgx", dsn, otelsql.WithAttributes())
	if err != nil {
		return nil, err
	}
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func registerRoutes(mux *http.ServeMux, validator security.TokenValidator, hierarchyHandler *hierarchy.Handler) {
	security.RegisterProtectedRoutes(mux, validator, hierarchyHandler.Routes())
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
