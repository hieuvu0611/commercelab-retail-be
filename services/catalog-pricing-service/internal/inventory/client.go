// Package inventory reads storefront availability from the inventory service.
package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

const (
	// batchSize is the most SKU ids the inventory availability endpoint accepts per call.
	batchSize        = 100
	maxResponseBytes = 1 << 20
)

// Client calls GET /api/v1/inventory/availability. It is safe for concurrent use.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient returns a client for the inventory service at baseURL. A nil httpClient uses http.DefaultClient;
// the caller's context carries the deadline.
func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient}
}

// States returns the state of each SKU. More than 100 SKUs are split into parallel batches; if any batch fails,
// the whole lookup fails (the caller then reports every availability as unknown, never a partial guess).
func (c *Client) States(ctx context.Context, skuIDs []string) (map[string]string, error) {
	out := make(map[string]string, len(skuIDs))
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		firstErr error
	)
	for start := 0; start < len(skuIDs); start += batchSize {
		batch := skuIDs[start:min(start+batchSize, len(skuIDs))]
		wg.Add(1)
		go func() {
			defer wg.Done()
			states, err := c.batch(ctx, batch)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = err
			}
			for sku, st := range states {
				out[sku] = st
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

func (c *Client) batch(ctx context.Context, skuIDs []string) (map[string]string, error) {
	endpoint := c.baseURL + "/api/v1/inventory/availability?warehouse=ALL&skuIds=" + url.QueryEscape(strings.Join(skuIDs, ","))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("inventory availability answered %d", res.StatusCode)
	}
	var body struct {
		Data struct {
			Items []struct {
				SkuID string `json:"skuId"`
				State string `json:"state"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxResponseBytes)).Decode(&body); err != nil {
		return nil, fmt.Errorf("inventory availability answered malformed JSON: %w", err)
	}
	states := make(map[string]string, len(body.Data.Items))
	for _, it := range body.Data.Items {
		switch it.State {
		case "IN_STOCK", "LOW", "OUT":
			states[it.SkuID] = it.State
		}
	}
	return states, nil
}
