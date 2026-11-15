package inventory

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStatesReadsTheBatchAndIgnoresUnknownStates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/inventory/availability" || r.URL.Query().Get("skuIds") != "A,B,C" {
			t.Errorf("unexpected request %s", r.URL.String())
		}
		fmt.Fprint(w, `{"data":{"warehouseId":"WH-HCM-01","items":[{"skuId":"A","state":"IN_STOCK"},{"skuId":"B","state":"LOW"},{"skuId":"C","state":"WEIRD"}]}}`)
	}))
	defer srv.Close()
	got, err := NewClient(srv.URL+"/", nil).States(context.Background(), []string{"A", "B", "C"})
	if err != nil || got["A"] != "IN_STOCK" || got["B"] != "LOW" {
		t.Fatalf("states = %v, err %v", got, err)
	}
	if _, ok := got["C"]; ok {
		t.Fatal("an unrecognised state must be dropped so the SKU reads as unknown")
	}
}

func TestStatesSplitsLargeLookupsIntoBatchesOfOneHundred(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		ids := strings.Split(r.URL.Query().Get("skuIds"), ",")
		if len(ids) > 100 {
			t.Errorf("batch of %d exceeds 100", len(ids))
		}
		var items []string
		for _, id := range ids {
			items = append(items, fmt.Sprintf(`{"skuId":%q,"state":"OUT"}`, id))
		}
		fmt.Fprintf(w, `{"data":{"items":[%s]}}`, strings.Join(items, ","))
	}))
	defer srv.Close()
	ids := make([]string, 250)
	for i := range ids {
		ids[i] = fmt.Sprintf("SKU-%d", i)
	}
	got, err := NewClient(srv.URL, nil).States(context.Background(), ids)
	if err != nil || len(got) != 250 || calls.Load() != 3 {
		t.Fatalf("got %d states, %d calls, err %v; want 250 states in 3 calls", len(got), calls.Load(), err)
	}
}

func TestStatesFailsOnErrorsTimeoutsAndMalformedBodies(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"500":       func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) },
		"malformed": func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{not json`) },
		"slow":      func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) },
	} {
		srv := httptest.NewServer(handler)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		if got, err := NewClient(srv.URL, nil).States(ctx, []string{"A"}); err == nil {
			t.Errorf("%s: states %v, want an error", name, got)
		}
		cancel()
		srv.Close()
	}
}
