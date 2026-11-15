package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/svcclient"
)

func serve(t *testing.T, status int, code string) (*Client, *map[string]string, *http.Header) {
	t.Helper()
	var got map[string]string
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		hdr = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": "x", "data": map[string]any{}})
	}))
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, svcclient.New(svcclient.Config{})), &got, &hdr
}

func TestHandoverSendsTheShipmentWarehouseAndForwardsTheCallersToken(t *testing.T) {
	c, body, hdr := serve(t, 200, "")
	if err := c.Handover(context.Background(), "tok", "O-1", "WH-HN-01"); err != nil {
		t.Fatal(err)
	}
	if (*body)["orderId"] != "O-1" || (*body)["warehouseId"] != "WH-HN-01" || hdr.Get("Authorization") != "Bearer tok" {
		t.Fatalf("body %v header %v", *body, *hdr)
	}
}

func TestHandoverMapsInventoryAnswers(t *testing.T) {
	for status, want := range map[int]error{403: ErrForbidden, 409: ErrRejected, 400: ErrRejected} {
		c, _, _ := serve(t, status, "X")
		if err := c.Handover(context.Background(), "tok", "O-1", "WH"); !errors.Is(err, want) {
			t.Errorf("status %d: err = %v, want %v", status, err, want)
		}
	}
	c, _, _ := serve(t, 503, "X")
	if err := c.Handover(context.Background(), "tok", "O-1", "WH"); !dependency.IsUnavailable(err) {
		t.Errorf("503: err = %v, want unavailable", err)
	}
}
