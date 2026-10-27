package main

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/config"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/middleware"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/s3"
	"github.com/hieuvu0611/commercelab-retail-be/services/ledger-reconciliation-service/internal/domain"
)

func main() {
	port := config.EnvOrDefault("LEDGER_RECONCILIATION_PORT", "8086")
	s3Client := s3.NewFromEnv()
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, "ledger-reconciliation-service healthy", map[string]any{
			"service":        "ledger-reconciliation-service",
			"postgresSchema": "ledger",
			"s3Bucket":       s3Client.Bucket,
		}, nil)
	})

	// B07, SC11: Đối soát sao kê cổng thanh toán (lưu trên AWS S3)
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
		result := domain.EvaluateSettlement(req.OrderID, req.VerifiedCapturedVND, 1, req.ActualPayoutVND, s3Client.ObjectURL(req.StatementFileKey))
		response.JSON(w, http.StatusOK, "Đối soát giao dịch hoàn tất (SC11)", result, nil)
	})

	log.Printf("[ledger-reconciliation-service] listening on :%s (schema: ledger, S3 bucket: %s)", port, s3Client.Bucket)
	log.Fatal(http.ListenAndServe(":"+port, middleware.Chain(mux)))
}
