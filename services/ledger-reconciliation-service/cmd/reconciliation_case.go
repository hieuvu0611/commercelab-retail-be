package main

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/ledger-reconciliation-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/ledger-reconciliation-service/internal/repository"
)

func registerCaseRoutes(
	mux *http.ServeMux,
	caseRepo *repository.CaseRepository,
	ledgerRepo *repository.LedgerRepository,
	settleRepo *repository.SettlementRepository,
) {
	financeAuth := func(h http.HandlerFunc) http.Handler {
		return auth.RequireAuth(auth.RequireRole("FINANCE", "FINANCE_MGR", "OPS_MGR", "ADMIN", "ACCESS_ADMIN")(h))
	}

	// GET /api/v1/admin/ledger/discrepancies - List discrepancy cases
	mux.Handle("GET /api/v1/admin/ledger/discrepancies", financeAuth(func(w http.ResponseWriter, r *http.Request) {
		status := r.URL.Query().Get("status")
		batchID := r.URL.Query().Get("batchId")
		orderID := r.URL.Query().Get("orderId")

		cases, err := caseRepo.ListCases(r.Context(), repository.CaseFilter{
			Status:  status,
			BatchID: batchID,
			OrderID: orderID,
		})
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to list discrepancy cases", nil)
			return
		}
		response.JSON(w, http.StatusOK, "Discrepancy cases retrieved", cases, nil)
	}))

	// GET /api/v1/admin/ledger/discrepancies/{id} - Get single case
	mux.Handle("GET /api/v1/admin/ledger/discrepancies/{id}", financeAuth(func(w http.ResponseWriter, r *http.Request) {
		caseID := r.PathValue("id")
		c, err := caseRepo.GetCase(r.Context(), caseID)
		if err != nil {
			if errors.Is(err, repository.ErrCaseNotFound) {
				response.Error(w, r, http.StatusNotFound, "NOT_FOUND", "Discrepancy case not found", nil)
				return
			}
			response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to get discrepancy case", nil)
			return
		}
		response.JSON(w, http.StatusOK, "Discrepancy case retrieved", c, nil)
	}))

	// POST /api/v1/admin/ledger/discrepancies/{id}/resolve - Resolve case with root cause classification & compensating ledger entry
	mux.Handle("POST /api/v1/admin/ledger/discrepancies/{id}/resolve", financeAuth(func(w http.ResponseWriter, r *http.Request) {
		caseID := r.PathValue("id")
		claims, _ := auth.ClaimsFrom(r.Context())
		actor := "FINANCE_MGR"
		if claims != nil {
			if claims.UserID != "" {
				actor = claims.UserID
			} else if claims.Subject != "" {
				actor = claims.Subject
			}
		}

		var req domain.ResolveCaseRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "Invalid JSON payload", nil)
			return
		}

		if req.CauseCategory == "" {
			response.Error(w, r, http.StatusBadRequest, "CATEGORY_REQUIRED", "Cause category is required", nil)
			return
		}
		if req.ResolutionNotes == "" {
			response.Error(w, r, http.StatusBadRequest, "NOTES_REQUIRED", "Resolution notes are required", nil)
			return
		}

		resolvedCase, journalEntry, err := caseRepo.ResolveCase(r.Context(), caseID, req, actor, ledgerRepo, settleRepo)
		if err != nil {
			if errors.Is(err, repository.ErrCaseNotFound) {
				response.Error(w, r, http.StatusNotFound, "NOT_FOUND", "Discrepancy case not found", nil)
				return
			}
			if errors.Is(err, repository.ErrCaseAlreadyResolved) {
				response.Error(w, r, http.StatusConflict, "ALREADY_RESOLVED", "Discrepancy case is already resolved", nil)
				return
			}
			response.Error(w, r, http.StatusInternalServerError, "RESOLVE_FAILED", err.Error(), nil)
			return
		}

		response.JSON(w, http.StatusOK, "Discrepancy case resolved with compensating journal entry", map[string]any{
			"case":         resolvedCase,
			"journalEntry": journalEntry,
		}, nil)
	}))
}
