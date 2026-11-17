package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/ledger-reconciliation-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/ledger-reconciliation-service/internal/repository"
)

func registerSettlementRoutes(mux *http.ServeMux, settleRepo *repository.SettlementRepository, ledgerRepo *repository.LedgerRepository, caseRepo *repository.CaseRepository) {
	financeAuth := func(h http.HandlerFunc) http.Handler {
		return auth.RequireAuth(auth.RequireRole("FINANCE", "FINANCE_MGR", "OPS_MGR", "ADMIN", "ACCESS_ADMIN")(h))
	}

	// POST /api/v1/admin/ledger/settlements/import - Import partner settlement CSV (idempotent)
	mux.Handle("POST /api/v1/admin/ledger/settlements/import", financeAuth(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFrom(r.Context())
		actor := "SYSTEM"
		if claims != nil {
			if claims.UserID != "" {
				actor = claims.UserID
			} else if claims.Subject != "" {
				actor = claims.Subject
			}
		}

		var reader io.Reader
		filename := "settlement.csv"
		s3Key := ""

		contentType := r.Header.Get("Content-Type")
		if strings.HasPrefix(contentType, "multipart/form-data") {
			if err := r.ParseMultipartForm(10 << 20); err != nil { // 10MB limit
				response.Error(w, r, http.StatusBadRequest, "INVALID_FORM", "Failed to parse multipart form", nil)
				return
			}
			file, handler, err := r.FormFile("file")
			if err != nil {
				response.Error(w, r, http.StatusBadRequest, "FILE_REQUIRED", "CSV file upload is required", nil)
				return
			}
			defer file.Close()
			reader = file
			filename = handler.Filename
			s3Key = r.FormValue("s3Key")
		} else if strings.HasPrefix(contentType, "application/json") {
			var body struct {
				Filename string `json:"filename"`
				S3Key    string `json:"s3Key"`
				Content  string `json:"content"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "Invalid JSON body", nil)
				return
			}
			if body.Filename != "" {
				filename = body.Filename
			}
			s3Key = body.S3Key
			reader = strings.NewReader(body.Content)
		} else {
			// Raw CSV stream in body
			reader = r.Body
			if fn := r.URL.Query().Get("filename"); fn != "" {
				filename = fn
			}
			s3Key = r.URL.Query().Get("s3Key")
		}

		batch, lines, err := domain.ParseSettlementCSV(reader, filename, s3Key, actor)
		if err != nil {
			response.Error(w, r, http.StatusBadRequest, "INVALID_CSV", err.Error(), nil)
			return
		}

		importedBatch, alreadyImported, err := settleRepo.ImportSettlementBatch(r.Context(), batch, lines)
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "IMPORT_ERROR", err.Error(), nil)
			return
		}

		msg := "Settlement batch imported successfully"
		statusCode := http.StatusCreated
		if alreadyImported {
			msg = "Settlement batch already imported (idempotent: zero duplicates)"
			statusCode = http.StatusOK
		}

		response.JSON(w, statusCode, msg, map[string]any{
			"batch":           importedBatch,
			"alreadyImported": alreadyImported,
			"linesCount":      len(lines),
		}, nil)
	}))

	// GET /api/v1/admin/ledger/settlements/batches - List settlement batches
	mux.Handle("GET /api/v1/admin/ledger/settlements/batches", financeAuth(func(w http.ResponseWriter, r *http.Request) {
		limit := 50
		offset := 0
		if l := r.URL.Query().Get("limit"); l != "" {
			if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
				limit = parsed
			}
		}
		if o := r.URL.Query().Get("offset"); o != "" {
			if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
				offset = parsed
			}
		}

		batches, total, err := settleRepo.ListSettlementBatches(r.Context(), limit, offset)
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to list batches", nil)
			return
		}

		response.JSON(w, http.StatusOK, "Settlement batches retrieved", map[string]any{
			"batches": batches,
			"total":   total,
			"limit":   limit,
			"offset":  offset,
		}, nil)
	}))

	// GET /api/v1/admin/ledger/settlements/batches/{id} - Get single batch
	mux.Handle("GET /api/v1/admin/ledger/settlements/batches/{id}", financeAuth(func(w http.ResponseWriter, r *http.Request) {
		batchID := r.PathValue("id")
		batch, err := settleRepo.GetSettlementBatch(r.Context(), batchID)
		if err != nil {
			if errors.Is(err, repository.ErrBatchNotFound) {
				response.Error(w, r, http.StatusNotFound, "NOT_FOUND", "Settlement batch not found", nil)
				return
			}
			response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusOK, "Settlement batch retrieved", batch, nil)
	}))

	// GET /api/v1/admin/ledger/settlements/batches/{id}/lines - List lines of batch
	mux.Handle("GET /api/v1/admin/ledger/settlements/batches/{id}/lines", financeAuth(func(w http.ResponseWriter, r *http.Request) {
		batchID := r.PathValue("id")
		lines, err := settleRepo.ListSettlementLines(r.Context(), batchID)
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to list lines", nil)
			return
		}
		response.JSON(w, http.StatusOK, "Settlement lines retrieved", lines, nil)
	}))

	// POST /api/v1/admin/ledger/settlements/batches/{id}/match - Execute matching engine against batch lines
	mux.Handle("POST /api/v1/admin/ledger/settlements/batches/{id}/match", financeAuth(func(w http.ResponseWriter, r *http.Request) {
		batchID := r.PathValue("id")
		claims, _ := auth.ClaimsFrom(r.Context())
		actor := "SYSTEM"
		if claims != nil {
			if claims.UserID != "" {
				actor = claims.UserID
			} else if claims.Subject != "" {
				actor = claims.Subject
			}
		}

		summary, err := settleRepo.MatchBatch(r.Context(), batchID, ledgerRepo, caseRepo, actor)
		if err != nil {
			if errors.Is(err, repository.ErrBatchNotFound) {
				response.Error(w, r, http.StatusNotFound, "NOT_FOUND", "Settlement batch not found", nil)
				return
			}
			response.Error(w, r, http.StatusInternalServerError, "MATCH_ERROR", err.Error(), nil)
			return
		}

		response.JSON(w, http.StatusOK, "Batch reconciliation matched successfully", summary, nil)
	}))

	// GET /api/v1/admin/ledger/reconciliations/summary - Global reconciliation overview
	mux.Handle("GET /api/v1/admin/ledger/reconciliations/summary", financeAuth(func(w http.ResponseWriter, r *http.Request) {
		overview, err := settleRepo.GetReconciliationOverview(r.Context())
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusOK, "Reconciliation overview retrieved", overview, nil)
	}))
}
