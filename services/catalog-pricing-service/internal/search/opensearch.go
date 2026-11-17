// Package search provides OpenSearch catalog indexing and faceted full-text queries (SC12 read-model).
package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const DefaultIndex = "catalog_products"

type ProductDoc struct {
	ID                string    `json:"id"`
	Slug              string    `json:"slug"`
	Name              string    `json:"name"`
	Description       string    `json:"description,omitempty"`
	Brand             string    `json:"brand"`
	PrimaryCategoryID string    `json:"primaryCategoryId"`
	CategoryIDs       []string  `json:"categoryIds"`
	MinPriceVND       int64     `json:"minPriceVnd"`
	MaxPriceVND       int64     `json:"maxPriceVnd"`
	SkuIDs            []string  `json:"skuIds"`
	PublishedAt       time.Time `json:"publishedAt"`
}

type FacetBucket struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}

type Facets struct {
	Brands     []FacetBucket `json:"brands,omitempty"`
	Categories []FacetBucket `json:"categories,omitempty"`
	MinPrice   *int64        `json:"minPriceVnd,omitempty"`
	MaxPrice   *int64        `json:"maxPriceVnd,omitempty"`
}

type Query struct {
	Text        string
	CategoryIDs []string
	MinPrice    *int64
	MaxPrice    *int64
}

type Result struct {
	ProductIDs []string
	Facets     Facets
}

type Client struct {
	baseURL string
	index   string
	http    *http.Client
}

func NewClient(baseURL string, httpClient *http.Client) *Client {
	if strings.TrimSpace(baseURL) == "" {
		return nil
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 3 * time.Second}
	}
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		index:   DefaultIndex,
		http:    httpClient,
	}
}

func (c *Client) IndexName() string { return c.index }

func (c *Client) EnsureIndex(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.baseURL+"/"+c.index, nil)
	if err != nil {
		return err
	}
	res, err := c.http.Do(req)
	if err == nil {
		res.Body.Close()
		if res.StatusCode == http.StatusOK {
			return nil
		}
	}
	mapping := map[string]any{
		"mappings": map[string]any{
			"properties": map[string]any{
				"id":                map[string]any{"type": "keyword"},
				"slug":              map[string]any{"type": "keyword"},
				"name":              map[string]any{"type": "text"},
				"description":       map[string]any{"type": "text"},
				"brand":             map[string]any{"type": "text", "fields": map[string]any{"keyword": map[string]any{"type": "keyword"}}},
				"primaryCategoryId": map[string]any{"type": "keyword"},
				"categoryIds":       map[string]any{"type": "keyword"},
				"minPriceVnd":       map[string]any{"type": "long"},
				"maxPriceVnd":       map[string]any{"type": "long"},
				"skuIds":            map[string]any{"type": "keyword"},
				"publishedAt":       map[string]any{"type": "date"},
			},
		},
	}
	body, _ := json.Marshal(mapping)
	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+"/"+c.index, bytes.NewReader(body))
	if err != nil {
		return err
	}
	putReq.Header.Set("Content-Type", "application/json")
	putRes, err := c.http.Do(putReq)
	if err != nil {
		return err
	}
	defer putRes.Body.Close()
	if putRes.StatusCode >= 300 && putRes.StatusCode != http.StatusBadRequest {
		raw, _ := io.ReadAll(io.LimitReader(putRes.Body, 4096))
		return fmt.Errorf("opensearch create index %d: %s", putRes.StatusCode, string(raw))
	}
	return nil
}

func (c *Client) BulkIndex(ctx context.Context, docs []ProductDoc) (int, error) {
	if err := c.EnsureIndex(ctx); err != nil {
		return 0, err
	}
	if len(docs) == 0 {
		return 0, nil
	}
	var buf bytes.Buffer
	for _, doc := range docs {
		metaLine, _ := json.Marshal(map[string]any{"index": map[string]any{"_index": c.index, "_id": doc.ID}})
		docLine, _ := json.Marshal(doc)
		buf.Write(metaLine)
		buf.WriteByte('\n')
		buf.Write(docLine)
		buf.WriteByte('\n')
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/_bulk?refresh=true", &buf)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	res, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return 0, fmt.Errorf("opensearch bulk failed %d: %s", res.StatusCode, string(raw))
	}
	return len(docs), nil
}

func (c *Client) Search(ctx context.Context, q Query) (Result, error) {
	must := []any{}
	filter := []any{}
	if strings.TrimSpace(q.Text) != "" {
		must = append(must, map[string]any{
			"multi_match": map[string]any{
				"query":     strings.TrimSpace(q.Text),
				"fields":    []string{"name^3", "brand^2", "description", "skuIds"},
				"fuzziness": "AUTO",
			},
		})
	} else {
		must = append(must, map[string]any{"match_all": map[string]any{}})
	}
	if len(q.CategoryIDs) > 0 {
		filter = append(filter, map[string]any{"terms": map[string]any{"categoryIds": q.CategoryIDs}})
	}
	if q.MinPrice != nil || q.MaxPrice != nil {
		rng := map[string]any{}
		if q.MinPrice != nil {
			rng["gte"] = *q.MinPrice
		}
		if q.MaxPrice != nil {
			rng["lte"] = *q.MaxPrice
		}
		filter = append(filter, map[string]any{"range": map[string]any{"minPriceVnd": rng}})
	}

	payload, _ := json.Marshal(map[string]any{
		"size":    500,
		"_source": []string{"id"},
		"query": map[string]any{
			"bool": map[string]any{
				"must":   must,
				"filter": filter,
			},
		},
		"aggs": map[string]any{
			"brands": map[string]any{
				"terms": map[string]any{"field": "brand.keyword", "size": 20},
			},
			"categories": map[string]any{
				"terms": map[string]any{"field": "primaryCategoryId", "size": 20},
			},
			"min_price": map[string]any{"min": map[string]any{"field": "minPriceVnd"}},
			"max_price": map[string]any{"max": map[string]any{"field": "maxPriceVnd"}},
		},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/"+c.index+"/_search", bytes.NewReader(payload))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return Result{}, fmt.Errorf("opensearch search failed %d: %s", res.StatusCode, string(raw))
	}

	var raw struct {
		Hits struct {
			Hits []struct {
				ID     string `json:"_id"`
				Source struct {
					ID string `json:"id"`
				} `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
		Aggregations struct {
			Brands struct {
				Buckets []struct {
					Key   string `json:"key"`
					Count int64  `json:"doc_count"`
				} `json:"buckets"`
			} `json:"brands"`
			Categories struct {
				Buckets []struct {
					Key   string `json:"key"`
					Count int64  `json:"doc_count"`
				} `json:"buckets"`
			} `json:"categories"`
			MinPrice struct {
				Value *float64 `json:"value"`
			} `json:"min_price"`
			MaxPrice struct {
				Value *float64 `json:"value"`
			} `json:"max_price"`
		} `json:"aggregations"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&raw); err != nil {
		return Result{}, err
	}

	out := Result{ProductIDs: make([]string, 0, len(raw.Hits.Hits))}
	for _, h := range raw.Hits.Hits {
		id := h.Source.ID
		if id == "" {
			id = h.ID
		}
		if id != "" {
			out.ProductIDs = append(out.ProductIDs, id)
		}
	}
	for _, b := range raw.Aggregations.Brands.Buckets {
		out.Facets.Brands = append(out.Facets.Brands, FacetBucket{Key: b.Key, Count: b.Count})
	}
	for _, c := range raw.Aggregations.Categories.Buckets {
		out.Facets.Categories = append(out.Facets.Categories, FacetBucket{Key: c.Key, Count: c.Count})
	}
	if raw.Aggregations.MinPrice.Value != nil {
		v := int64(*raw.Aggregations.MinPrice.Value)
		out.Facets.MinPrice = &v
	}
	if raw.Aggregations.MaxPrice.Value != nil {
		v := int64(*raw.Aggregations.MaxPrice.Value)
		out.Facets.MaxPrice = &v
	}
	return out, nil
}
