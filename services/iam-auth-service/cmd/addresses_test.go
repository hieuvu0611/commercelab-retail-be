package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/services/iam-auth-service/internal/repository"
)

const addressBody = `{"recipient":"Lan Nguyen","phone":"+84 (90) 123-4567","line1":"12 Nguyen Hue","ward":"Ben Nghe","district":"District 1","province":"Ho Chi Minh City"}`

func addressServer(t *testing.T) http.Handler {
	t.Helper()
	authtest.Configure(t)
	return NewServerFromDeps(Deps{Users: newFakeStore(t), Tokens: newFakeTokens(), DBConnected: true, Addresses: repository.NewMemoryAddressStore()})
}

func customerToken(t *testing.T, id string) string {
	t.Helper()
	tok, err := auth.SignAccessToken(auth.Identity{UserID: id, Roles: []string{"CUSTOMER"}})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

type addressResp struct {
	Data addressJSON `json:"data"`
}

type addressListResp struct {
	Data []addressJSON `json:"data"`
}

func TestAddressEndpointsRequireAuthentication(t *testing.T) {
	srv := addressServer(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/users/me/addresses"},
		{http.MethodPost, "/api/v1/users/me/addresses"},
		{http.MethodGet, "/api/v1/users/me/addresses/a-1"},
		{http.MethodPut, "/api/v1/users/me/addresses/a-1"},
		{http.MethodDelete, "/api/v1/users/me/addresses/a-1"},
	} {
		if rec := authedRequest(t, srv, c.method, c.path, "", addressBody); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: %d", c.method, c.path, rec.Code)
		}
	}
}

func TestCreateListUpdateAndDeleteAnAddress(t *testing.T) {
	srv := addressServer(t)
	alice := customerToken(t, "alice")

	rec := authedRequest(t, srv, http.MethodPost, "/api/v1/users/me/addresses", alice, addressBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	created := decodeBody[addressResp](t, rec).Data
	if !created.IsDefault || created.Phone != "+84901234567" || created.ID == "" {
		t.Fatalf("created = %+v; want default and normalized phone", created)
	}

	rec = authedRequest(t, srv, http.MethodPut, "/api/v1/users/me/addresses/"+created.ID, alice,
		strings.Replace(addressBody, "Lan Nguyen", "  Lan N.  ", 1))
	if got := decodeBody[addressResp](t, rec).Data; rec.Code != http.StatusOK || got.Recipient != "Lan N." {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	if rec = authedRequest(t, srv, http.MethodGet, "/api/v1/users/me/addresses/"+created.ID, alice, ""); rec.Code != http.StatusOK {
		t.Fatalf("get: %d", rec.Code)
	}
	if list := decodeBody[addressListResp](t, authedRequest(t, srv, http.MethodGet, "/api/v1/users/me/addresses", alice, "")).Data; len(list) != 1 {
		t.Fatalf("list = %+v", list)
	}
	if rec = authedRequest(t, srv, http.MethodDelete, "/api/v1/users/me/addresses/"+created.ID, alice, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec = authedRequest(t, srv, http.MethodDelete, "/api/v1/users/me/addresses/"+created.ID, alice, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("second delete: %d", rec.Code)
	}
}

func TestAnotherUsersAddressLooksMissing(t *testing.T) {
	srv := addressServer(t)
	alice, bob := customerToken(t, "alice"), customerToken(t, "bob")
	created := decodeBody[addressResp](t, authedRequest(t, srv, http.MethodPost, "/api/v1/users/me/addresses", alice, addressBody)).Data

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		rec := authedRequest(t, srv, method, "/api/v1/users/me/addresses/"+created.ID, bob, addressBody)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "ADDRESS_NOT_FOUND") {
			t.Fatalf("%s as another user: %d %s", method, rec.Code, rec.Body)
		}
	}
	if list := decodeBody[addressListResp](t, authedRequest(t, srv, http.MethodGet, "/api/v1/users/me/addresses", bob, "")).Data; len(list) != 0 {
		t.Fatalf("bob sees %+v", list)
	}
	// Identity headers never select the owner.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me/addresses", nil)
	req.Header.Set("Authorization", "Bearer "+bob)
	req.Header.Set("X-User-ID", "alice")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), created.ID) {
		t.Fatalf("X-User-ID leaked an address: %s", rec.Body)
	}
}

func TestAddressValidation(t *testing.T) {
	srv := addressServer(t)
	alice := customerToken(t, "alice")
	long := strings.Repeat("x", 201)
	tests := map[string]struct{ body, field string }{
		"missing recipient": {`{"phone":"+84901234567","line1":"a","ward":"b","district":"c","province":"d"}`, "recipient"},
		"bad phone":         {strings.Replace(addressBody, "+84 (90) 123-4567", "call me", 1), "phone"},
		"missing phone":     {strings.Replace(addressBody, `"phone":"+84 (90) 123-4567",`, "", 1), "phone"},
		"long line":         {strings.Replace(addressBody, "12 Nguyen Hue", long, 1), "line1"},
		"blank ward":        {strings.Replace(addressBody, "Ben Nghe", "   ", 1), "ward"},
		"missing province":  {strings.Replace(addressBody, `,"province":"Ho Chi Minh City"`, "", 1), "province"},
	}
	for name, tc := range tests {
		rec := authedRequest(t, srv, http.MethodPost, "/api/v1/users/me/addresses", alice, tc.body)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"`+tc.field+`"`) {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := authedRequest(t, srv, http.MethodPost, "/api/v1/users/me/addresses", alice, `{"unknown":1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", rec.Code)
	}
}

func TestAddressBookIsCappedAndDefaultSwitches(t *testing.T) {
	srv := addressServer(t)
	alice := customerToken(t, "alice")
	var ids []string
	for range repository.MaxAddresses {
		rec := authedRequest(t, srv, http.MethodPost, "/api/v1/users/me/addresses", alice, addressBody)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", rec.Code, rec.Body)
		}
		ids = append(ids, decodeBody[addressResp](t, rec).Data.ID)
	}
	rec := authedRequest(t, srv, http.MethodPost, "/api/v1/users/me/addresses", alice, addressBody)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "ADDRESS_LIMIT_REACHED") {
		t.Fatalf("11th: %d %s", rec.Code, rec.Body)
	}

	made := authedRequest(t, srv, http.MethodPut, "/api/v1/users/me/addresses/"+ids[4], alice, strings.Replace(addressBody, `}`, `,"isDefault":true}`, 1))
	if !decodeBody[addressResp](t, made).Data.IsDefault {
		t.Fatalf("make default: %s", made.Body)
	}
	list := decodeBody[addressListResp](t, authedRequest(t, srv, http.MethodGet, "/api/v1/users/me/addresses", alice, "")).Data
	defaults := 0
	for _, a := range list {
		if a.IsDefault {
			defaults++
		}
	}
	if list[0].ID != ids[4] || defaults != 1 {
		t.Fatalf("default must be first and unique: %+v", list)
	}
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return out
}
