package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/config"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/eventconsumer"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/middleware"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/migrate"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/outbox"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/redact"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/s3"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/telemetry"
	"github.com/hieuvu0611/commercelab-retail-be/services/ledger-reconciliation-service/internal/consumer"
	"github.com/hieuvu0611/commercelab-retail-be/services/ledger-reconciliation-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/ledger-reconciliation-service/internal/repository"
	"github.com/hieuvu0611/commercelab-retail-be/services/ledger-reconciliation-service/migrations"
)

func NewServer(
	repo *repository.LedgerRepository,
	settleRepo *repository.SettlementRepository,
	caseRepo *repository.CaseRepository,
	s3Client *s3.Client,
	pgConnected bool,
) http.Handler {
	if caseRepo == nil {
		caseRepo = repository.NewCaseRepository(nil)
	}
	mux := http.NewServeMux()

	mux.Handle("GET /metrics", telemetry.DefaultRegistry().Handler())

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		bucket := ""
		if s3Client != nil {
			bucket = s3Client.Bucket
		}
		response.JSON(w, http.StatusOK, "ledger-reconciliation-service healthy", map[string]any{
			"service":        "ledger-reconciliation-service",
			"postgresSchema": "ledger",
			"s3Bucket":       bucket,
			"pgConnected":    pgConnected,
		}, nil)
	})

	// B07, SC11: Đối soát sao kê cổng thanh toán (legacy test endpoint)
	mux.HandleFunc("POST /api/v1/reconciliations/evaluate", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			OrderID             string `json:"orderId"`
			VerifiedCapturedVND int64  `json:"verifiedCapturedVnd"`
			ActualPayoutVND     int64  `json:"actualPayoutVnd"`
			StatementFileKey    string `json:"statementFileKey"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "Dữ liệu đối soát không hợp lệ", nil)
			return
		}
		if req.StatementFileKey == "" {
			req.StatementFileKey = "settlements/2026-10-26-gateway.csv"
		}
		s3URL := req.StatementFileKey
		if s3Client != nil {
			s3URL = s3Client.ObjectURL(req.StatementFileKey)
		}
		result := domain.EvaluateSettlement(req.OrderID, req.VerifiedCapturedVND, 1, req.ActualPayoutVND, s3URL)
		response.JSON(w, http.StatusOK, "Đối soát giao dịch hoàn tất (SC11)", result, nil)
	})

	// Register P10 Ledger, Settlement & Case APIs
	registerLedgerRoutes(mux, repo)
	registerSettlementRoutes(mux, settleRepo, repo, caseRepo)
	registerCaseRoutes(mux, caseRepo, repo, settleRepo)

	return middleware.Chain(mux)
}

func main() {
	redact.InstallDefaultLogger()
	tracer := telemetry.Configure("ledger-reconciliation-service", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	defer func() { _ = tracer.Shutdown(context.Background()) }()
	port := config.EnvOrDefault("LEDGER_RECONCILIATION_PORT", "8086")
	pgURL := config.EnvOrDefault("DATABASE_URL_LEDGER", config.EnvOrDefault("DATABASE_URL", "postgres://ledger_svc_user:ledger_secret@localhost:5432/commerce_lab?sslmode=disable&search_path=ledger"))
	secrets := config.NewLoader()
	auth.LoadSecrets(secrets)
	if err := secrets.Err(); err != nil {
		log.Fatalf("[ledger-reconciliation-service] %v", err)
	}
	s3Client := s3.NewFromEnv()

	ctx := context.Background()

	if migrate.EnabledOnStart() {
		if _, err := migrate.Up(ctx, pgURL, migrations.FS); err != nil {
			log.Fatalf("[ledger-reconciliation-service] migrations failed: %v", err)
		}
	}

	pgPool, err := db.ConnectPostgres(ctx, pgURL)
	if err := dependency.CheckStartup("ledger-reconciliation-service", "postgres", err); err != nil {
		log.Printf("[ledger-reconciliation-service] postgres not available (%v); operating in-memory", err)
	}
	if pgPool != nil {
		defer pgPool.Close()
	}

	repo := repository.NewLedgerRepository(pgPool)
	settleRepo := repository.NewSettlementRepository(pgPool)
	caseRepo := repository.NewCaseRepository(pgPool)

	posConsumer := &consumer.POSHandler{Repo: repo}
	go func() {
		err := eventconsumer.Run(ctx, eventconsumer.Config{
			Brokers: config.EnvOrDefault("REDPANDA_BROKERS", "localhost:19092"),
			Group:   "ledger-reconciliation-service-pos",
			Topic:   outbox.TopicFor("Order"),
		}, posConsumer.Handle)
		if err != nil {
			log.Printf("[ledger-reconciliation-service] POS event consumer stopped: %v", err)
		}
	}()

	server := NewServer(repo, settleRepo, caseRepo, s3Client, pgPool != nil)

	log.Printf("[ledger-reconciliation-service] listening on :%s (schema: ledger, S3 bucket: %s)", port, s3Client.Bucket)
	log.Fatal(http.ListenAndServe(":"+port, server))
}
