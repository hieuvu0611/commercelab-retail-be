package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/request"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/iam-auth-service/internal/repository"
)

// AdminStore is what staff administration needs (implemented by repository.AdminRepository).
type AdminStore interface {
	CreateStaff(ctx context.Context, in repository.CreateStaffInput) (repository.User, error)
	SetRoles(ctx context.Context, actorID, targetID string, grants []repository.RoleGrant, reason string) (repository.User, error)
	Deactivate(ctx context.Context, actorID, targetID, reason string) (repository.User, error)
	ListUsers(ctx context.Context, f repository.ListFilter) ([]repository.User, int, error)
}

const (
	minReasonLength  = 3
	maxReasonLength  = 500
	defaultPageLimit = 25
	maxPageLimit     = 100
)

// grantJSON is a role grant on the wire.
type grantJSON struct {
	Role        string `json:"role"`
	WarehouseID string `json:"warehouseId,omitempty"`
}

// adminUserJSON is a user as the administration screens see it.
type adminUserJSON struct {
	ID       string      `json:"id"`
	Email    string      `json:"email"`
	FullName string      `json:"fullName"`
	Phone    string      `json:"phone,omitempty"`
	IsActive bool        `json:"isActive"`
	Roles    []grantJSON `json:"roles"`
}

func toAdminUser(u repository.User) adminUserJSON {
	roles := make([]grantJSON, 0, len(u.Grants))
	for _, g := range u.Grants {
		roles = append(roles, grantJSON{Role: g.Role, WarehouseID: g.WarehouseID})
	}
	return adminUserJSON{ID: u.ID, Email: u.Email, FullName: u.Name, Phone: u.Phone, IsActive: u.IsActive, Roles: roles}
}

func toGrants(in []grantJSON) []repository.RoleGrant {
	grants := make([]repository.RoleGrant, 0, len(in))
	for _, g := range in {
		grants = append(grants, repository.RoleGrant{Role: strings.TrimSpace(g.Role), WarehouseID: strings.TrimSpace(g.WarehouseID)})
	}
	return grants
}

func validateReason(reason string) []string {
	n := utf8.RuneCountInString(strings.TrimSpace(reason))
	if n < minReasonLength || n > maxReasonLength {
		return []string{"Reason is required (3 to 500 characters)"}
	}
	return nil
}

// writeAdminError maps repository errors to HTTP answers.
func writeAdminError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case dependency.IsUnavailable(err):
		dependency.WriteUnavailable(w, r, err)
	case errors.Is(err, repository.ErrSelfModification):
		response.Error(w, r, http.StatusForbidden, "CANNOT_MODIFY_OWN_ACCESS", "Administrators cannot change their own access", nil)
	case errors.Is(err, repository.ErrUserNotFound):
		response.Error(w, r, http.StatusNotFound, "USER_NOT_FOUND", "The user does not exist", nil)
	case errors.Is(err, repository.ErrEmailTaken):
		response.Error(w, r, http.StatusConflict, "EMAIL_ALREADY_REGISTERED", "An account with this email already exists", nil)
	case errors.Is(err, repository.ErrUnknownRole):
		response.Error(w, r, http.StatusUnprocessableEntity, "VALIDATION_FAILED", "Request validation failed",
			map[string][]string{"roles": {"Unknown role"}})
	default:
		response.Error(w, r, http.StatusInternalServerError, "ADMIN_OPERATION_FAILED", "The operation could not be completed", nil)
	}
}

// registerAdminRoutes adds the ACCESS_ADMIN-only staff administration API (P1-T09, B09).
func registerAdminRoutes(mux *http.ServeMux, admin AdminStore) {
	adminOnly := func(h http.HandlerFunc) http.Handler {
		return auth.RequireAuth(auth.RequireRole("ACCESS_ADMIN")(h))
	}
	actorID := func(r *http.Request) string {
		claims, _ := auth.ClaimsFrom(r.Context())
		return claims.UserID
	}

	mux.Handle("GET /api/v1/admin/users", adminOnly(func(w http.ResponseWriter, r *http.Request) {
		limit, offset := defaultPageLimit, 0
		if v := r.URL.Query().Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > maxPageLimit {
				response.Error(w, r, http.StatusUnprocessableEntity, "VALIDATION_FAILED", "Request validation failed",
					map[string][]string{"limit": {"Limit must be between 1 and 100"}})
				return
			}
			limit = n
		}
		if v := r.URL.Query().Get("offset"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				response.Error(w, r, http.StatusUnprocessableEntity, "VALIDATION_FAILED", "Request validation failed",
					map[string][]string{"offset": {"Offset must be zero or more"}})
				return
			}
			offset = n
		}
		users, total, err := admin.ListUsers(r.Context(), repository.ListFilter{
			StaffOnly: r.URL.Query().Get("staff") == "true", Limit: limit, Offset: offset,
		})
		if err != nil {
			writeAdminError(w, r, err)
			return
		}
		items := make([]adminUserJSON, 0, len(users))
		for _, u := range users {
			items = append(items, toAdminUser(u))
		}
		pageCount := (total + limit - 1) / limit
		response.JSON(w, http.StatusOK, "Users retrieved", items, &response.Metadata{
			Total: int64(total), Page: offset/limit + 1, PageCount: pageCount, Limit: limit,
		})
	}))

	mux.Handle("POST /api/v1/admin/users", adminOnly(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Email    string      `json:"email"`
			FullName string      `json:"fullName"`
			Password string      `json:"password"`
			Roles    []grantJSON `json:"roles"`
			Reason   string      `json:"reason"`
		}
		if !request.DecodeJSON(w, r, &req) {
			return
		}
		email := normalizeEmail(req.Email)
		fieldErrors := validateRegistration(email, req.Password, req.FullName)
		if problems := repository.ValidGrants(toGrants(req.Roles)); len(problems) != 0 {
			fieldErrors["roles"] = problems
		}
		if problems := validateReason(req.Reason); len(problems) != 0 {
			fieldErrors["reason"] = problems
		}
		if len(fieldErrors) != 0 {
			response.Error(w, r, http.StatusUnprocessableEntity, "VALIDATION_FAILED", "Request validation failed", fieldErrors)
			return
		}
		u, err := admin.CreateStaff(r.Context(), repository.CreateStaffInput{
			ActorID: actorID(r), Email: email, FullName: strings.TrimSpace(req.FullName), Password: req.Password,
			Grants: toGrants(req.Roles), Reason: strings.TrimSpace(req.Reason),
		})
		if err != nil {
			writeAdminError(w, r, err)
			return
		}
		response.JSON(w, http.StatusCreated, "User created", toAdminUser(u), nil)
	}))

	mux.Handle("PUT /api/v1/admin/users/{id}/roles", adminOnly(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Roles  []grantJSON `json:"roles"`
			Reason string      `json:"reason"`
		}
		if !request.DecodeJSON(w, r, &req) {
			return
		}
		fieldErrors := map[string][]string{}
		if problems := repository.ValidGrants(toGrants(req.Roles)); len(problems) != 0 {
			fieldErrors["roles"] = problems
		}
		if problems := validateReason(req.Reason); len(problems) != 0 {
			fieldErrors["reason"] = problems
		}
		if len(fieldErrors) != 0 {
			response.Error(w, r, http.StatusUnprocessableEntity, "VALIDATION_FAILED", "Request validation failed", fieldErrors)
			return
		}
		target := r.PathValue("id")
		if target == actorID(r) {
			writeAdminError(w, r, repository.ErrSelfModification)
			return
		}
		u, err := admin.SetRoles(r.Context(), actorID(r), target, toGrants(req.Roles), strings.TrimSpace(req.Reason))
		if err != nil {
			writeAdminError(w, r, err)
			return
		}
		response.JSON(w, http.StatusOK, "Roles updated", toAdminUser(u), nil)
	}))

	mux.Handle("POST /api/v1/admin/users/{id}/deactivate", adminOnly(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Reason string `json:"reason"`
		}
		if !request.DecodeJSON(w, r, &req) {
			return
		}
		if problems := validateReason(req.Reason); len(problems) != 0 {
			response.Error(w, r, http.StatusUnprocessableEntity, "VALIDATION_FAILED", "Request validation failed",
				map[string][]string{"reason": problems})
			return
		}
		target := r.PathValue("id")
		if target == actorID(r) {
			writeAdminError(w, r, repository.ErrSelfModification)
			return
		}
		u, err := admin.Deactivate(r.Context(), actorID(r), target, strings.TrimSpace(req.Reason))
		if err != nil {
			writeAdminError(w, r, err)
			return
		}
		response.JSON(w, http.StatusOK, "User deactivated", toAdminUser(u), nil)
	}))
}
