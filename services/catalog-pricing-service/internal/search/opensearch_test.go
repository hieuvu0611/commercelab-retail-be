package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOpenSearchBulkIndexAndFacetSearch(t *testing.T) {
	var seenBulk, seenSearch bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead && r.URL.Path == "/catalog_products":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/_bulk"):
			seenBulk = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"errors":false,"items":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/catalog_products/_search":
			seenSearch = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"hits":{"hits":[{"_id":"PROD-1","_source":{"id":"PROD-1"}}]},
				"aggregations":{
					"brands":{"buckets":[{"key":"CommerceLab","doc_count":1}]},
					"categories":{"buckets":[{"key":"CAT-BALO","doc_count":1}]},
					"min_price":{"value":300000},
					"max_price":{"value":350000}
				}
			}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, srv.Client())
	n, err := c.BulkIndex(context.Background(), []ProductDoc{{
		ID: "PROD-1", Slug: "city-backpack", Name: "City Backpack", Brand: "CommerceLab",
		PrimaryCategoryID: "CAT-BALO", MinPriceVND: 300000, MaxPriceVND: 350000,
		PublishedAt: time.Now().UTC(),
	}})
	if err != nil || n != 1 || !seenBulk {
		t.Fatalf("BulkIndex failed: n=%d err=%v seenBulk=%v", n, err, seenBulk)
	}

	res, err := c.Search(context.Background(), Query{Text: "backpack"})
	if err != nil || !seenSearch || len(res.ProductIDs) != 1 || len(res.Facets.Brands) != 1 {
		t.Fatalf("Search unexpected: res=%+v err=%v", res, err)
	}
}
