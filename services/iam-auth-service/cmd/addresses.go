package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/request"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/iam-auth-service/internal/repository"
)

const (
	maxRecipientLength = 100
	maxLine1Length     = 200
	maxAreaLength      = 100
)

type addressAPI struct {
	store repository.AddressStore
}

// addressJSON is an address on the wire. It carries personal data: it is returned only to its owner and is
// never logged.
type addressJSON struct {
	ID        string    `json:"id"`
	Recipient string    `json:"recipient"`
	Phone     string    `json:"phone"`
	Line1     string    `json:"line1"`
	Ward      string    `json:"ward"`
	District  string    `json:"district"`
	Province  string    `json:"province"`
	IsDefault bool      `json:"isDefault"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func toAddressJSON(a repository.Address) addressJSON {
	return addressJSON{ID: a.ID, Recipient: a.Recipient, Phone: a.Phone, Line1: a.Line1, Ward: a.Ward,
		District: a.District, Province: a.Province, IsDefault: a.IsDefault, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt}
}

// registerAddressRoutes serves the signed-in customer's address book. The owner is always the token subject.
func registerAddressRoutes(mux *http.ServeMux, store repository.AddressStore) {
	api := &addressAPI{store: store}
	mine := func(h http.HandlerFunc) http.Handler { return auth.RequireAuth(h) }
	mux.Handle("GET /api/v1/users/me/addresses", mine(api.list))
	mux.Handle("POST /api/v1/users/me/addresses", mine(api.create))
	mux.Handle("GET /api/v1/users/me/addresses/{id}", mine(api.get))
	mux.Handle("PUT /api/v1/users/me/addresses/{id}", mine(api.update))
	mux.Handle("DELETE /api/v1/users/me/addresses/{id}", mine(api.remove))
}

func (a *addressAPI) owner(r *http.Request) string {
	claims, _ := auth.ClaimsFrom(r.Context())
	return claims.UserID
}

func (a *addressAPI) list(w http.ResponseWriter, r *http.Request) {
	list, err := a.store.List(r.Context(), a.owner(r))
	if err != nil {
		writeAddressError(w, r, err)
		return
	}
	out := make([]addressJSON, len(list))
	for i, item := range list {
		out[i] = toAddressJSON(item)
	}
	response.JSON(w, http.StatusOK, "Addresses", out, nil)
}

func (a *addressAPI) get(w http.ResponseWriter, r *http.Request) {
	item, err := a.store.Get(r.Context(), a.owner(r), r.PathValue("id"))
	if err != nil {
		writeAddressError(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, "Address", toAddressJSON(item), nil)
}

func (a *addressAPI) create(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeAddress(w, r)
	if !ok {
		return
	}
	item, err := a.store.Create(r.Context(), a.owner(r), in)
	if err != nil {
		writeAddressError(w, r, err)
		return
	}
	response.JSON(w, http.StatusCreated, "Address created", toAddressJSON(item), nil)
}

func (a *addressAPI) update(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeAddress(w, r)
	if !ok {
		return
	}
	item, err := a.store.Update(r.Context(), a.owner(r), r.PathValue("id"), in)
	if err != nil {
		writeAddressError(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, "Address updated", toAddressJSON(item), nil)
}

func (a *addressAPI) remove(w http.ResponseWriter, r *http.Request) {
	if err := a.store.Delete(r.Context(), a.owner(r), r.PathValue("id")); err != nil {
		writeAddressError(w, r, err)
		return
	}
	response.JSON[any](w, http.StatusNoContent, "", nil, nil)
}

// decodeAddress reads and validates the body; it writes the error response itself and returns false on failure.
func decodeAddress(w http.ResponseWriter, r *http.Request) (repository.AddressInput, bool) {
	var body struct {
		Recipient string `json:"recipient"`
		Phone     string `json:"phone"`
		Line1     string `json:"line1"`
		Ward      string `json:"ward"`
		District  string `json:"district"`
		Province  string `json:"province"`
		IsDefault bool   `json:"isDefault"`
	}
	if !request.DecodeJSON(w, r, &body) {
		return repository.AddressInput{}, false
	}
	in := repository.AddressInput{
		Recipient: strings.TrimSpace(body.Recipient), Phone: normalizePhone(body.Phone), Line1: strings.TrimSpace(body.Line1),
		Ward: strings.TrimSpace(body.Ward), District: strings.TrimSpace(body.District), Province: strings.TrimSpace(body.Province),
		IsDefault: body.IsDefault,
	}
	fieldErrors := map[string][]string{}
	requireText := func(field, label, value string, limit int) {
		switch {
		case value == "":
			fieldErrors[field] = []string{label + " is required"}
		case utf8.RuneCountInString(value) > limit:
			fieldErrors[field] = []string{label + " must be at most " + strconv.Itoa(limit) + " characters"}
		}
	}
	requireText("recipient", "Recipient", in.Recipient, maxRecipientLength)
	requireText("line1", "Address line", in.Line1, maxLine1Length)
	requireText("ward", "Ward", in.Ward, maxAreaLength)
	requireText("district", "District", in.District, maxAreaLength)
	requireText("province", "Province", in.Province, maxAreaLength)
	if !validPhone(in.Phone) {
		fieldErrors["phone"] = []string{"Phone must be an international number of 7 to 15 digits"}
	}
	if len(fieldErrors) != 0 {
		response.Error(w, r, http.StatusUnprocessableEntity, "VALIDATION_FAILED", "Request validation failed", fieldErrors)
		return repository.AddressInput{}, false
	}
	return in, true
}

func writeAddressError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, repository.ErrAddressNotFound):
		response.Error(w, r, http.StatusNotFound, "ADDRESS_NOT_FOUND", "The address does not exist", nil)
	case errors.Is(err, repository.ErrAddressLimit):
		response.Error(w, r, http.StatusConflict, "ADDRESS_LIMIT_REACHED", "The address book holds at most 10 addresses", nil)
	case errors.Is(err, repository.ErrUserNotFound):
		response.Error(w, r, http.StatusNotFound, "USER_NOT_FOUND", "The account does not exist", nil)
	case dependency.IsUnavailable(err):
		dependency.WriteUnavailable(w, r, err)
	default:
		response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", nil)
	}
}
