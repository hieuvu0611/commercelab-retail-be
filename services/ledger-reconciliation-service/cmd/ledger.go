package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/ledger-reconciliation-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/ledger-reconciliation-service/internal/repository"
)

func registerLedgerRoutes(mux *http.ServeMux, repo *repository.LedgerRepository) {
	financeAuth := func(h http.HandlerFunc) http.Handler {
		return auth.RequireAuth(auth.RequireRole("FINANCE", "FINANCE_MGR", "OPS_MGR", "ADMIN", "ACCESS_ADMIN")(h))
	}

	// GET /api/v1/admin/ledger/accounts - List Chart of Accounts with balances
	mux.Handle("GET /api/v1/admin/ledger/accounts", financeAuth(func(w http.ResponseWriter, r *http.Request) {
		accounts, err := repo.ListAccounts(r.Context())
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to list accounts", nil)
			return
		}
		response.JSON(w, http.StatusOK, "Accounts retrieved", accounts, nil)
	}))

	// GET /api/v1/admin/ledger/entries - List Journal Entries
	mux.Handle("GET /api/v1/admin/ledger/entries", financeAuth(func(w http.ResponseWriter, r *http.Request) {
		refID := r.URL.Query().Get("referenceId")
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

		entries, total, err := repo.ListJournalEntries(r.Context(), refID, limit, offset)
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to list journal entries", nil)
			return
		}
		response.JSON(w, http.StatusOK, "Journal entries retrieved", map[string]any{
			"entries": entries,
			"total":   total,
			"limit":   limit,
			"offset":  offset,
		}, nil)
	}))

	// GET /api/v1/admin/ledger/entries/{id} - Get single journal entry
	mux.Handle("GET /api/v1/admin/ledger/entries/{id}", financeAuth(func(w http.ResponseWriter, r *http.Request) {
		entryID := r.PathValue("id")
		entry, err := repo.GetJournalEntry(r.Context(), entryID)
		if err != nil {
			if errors.Is(err, repository.ErrEntryNotFound) {
				response.Error(w, r, http.StatusNotFound, "NOT_FOUND", "Journal entry not found", nil)
				return
			}
			response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to get entry", nil)
			return
		}
		response.JSON(w, http.StatusOK, "Journal entry retrieved", entry, nil)
	}))

	// POST /api/v1/admin/ledger/entries - Record a new Journal Entry
	mux.Handle("POST /api/v1/admin/ledger/entries", financeAuth(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFrom(r.Context())
		actor := "SYSTEM"
		if claims != nil {
			if claims.UserID != "" {
				actor = claims.UserID
			} else if claims.Subject != "" {
				actor = claims.Subject
			}
		}

		var req struct {
			EntryID     string `json:"entryId"`
			EventType   string `json:"eventType"`
			ReferenceID string `json:"referenceId"`
			Description string `json:"description"`
			Postings    []struct {
				AccountCode string `json:"accountCode"`
				Direction   string `json:"direction"`
				AmountVND   int64  `json:"amountVnd"`
			} `json:"postings"`
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "Invalid JSON payload", nil)
			return
		}

		if req.EntryID == "" {
			req.EntryID = fmt.Sprintf("JE-%d", time.Now().UnixNano()%100000000)
		}

		postings := make([]domain.Posting, 0, len(req.Postings))
		for _, p := range req.Postings {
			postings = append(postings, domain.Posting{
				AccountCode: p.AccountCode,
				Direction:   domain.PostingDirection(strings.ToUpper(p.Direction)),
				AmountVND:   p.AmountVND,
			})
		}

		entry := domain.JournalEntry{
			EntryID:     req.EntryID,
			EventType:   req.EventType,
			ReferenceID: req.ReferenceID,
			Description: req.Description,
			PostedAt:    time.Now().UTC(),
			CreatedBy:   actor,
			Postings:    postings,
		}

		recorded, err := repo.RecordJournalEntry(r.Context(), entry)
		if err != nil {
			if errors.Is(err, domain.ErrUnbalancedEntry) || errors.Is(err, domain.ErrInsufficientLines) || errors.Is(err, domain.ErrInvalidPosting) || errors.Is(err, domain.ErrMissingField) {
				response.Error(w, r, http.StatusBadRequest, "INVALID_ENTRY", err.Error(), nil)
				return
			}
			if errors.Is(err, repository.ErrAccountNotFound) {
				response.Error(w, r, http.StatusBadRequest, "ACCOUNT_NOT_FOUND", err.Error(), nil)
				return
			}
			response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
			return
		}

		response.JSON(w, http.StatusCreated, "Journal entry recorded", recorded, nil)
	}))
}
