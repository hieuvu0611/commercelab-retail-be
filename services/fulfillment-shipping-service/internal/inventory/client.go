// Package inventory calls the inventory service on behalf of a warehouse user.
package inventory

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/svcclient"
)

var (
	// ErrForbidden means inventory refused the caller's role or warehouse scope.
	ErrForbidden = errors.New("inventory refused the caller's warehouse scope")
	// ErrRejected means inventory cannot hand the order over (no committed stock, different warehouse).
	ErrRejected = errors.New("inventory rejected the handover")
	// ErrStockLeft means inventory refused to release the stock because it was already handed over (SC06 branch B).
	ErrStockLeft = errors.New("the stock was already handed over")
)

// Client is the HTTP client of the inventory service.
type Client struct {
	base string
	c    *svcclient.Client
}

func NewClient(baseURL string, c *svcclient.Client) *Client {
	return &Client{base: strings.TrimRight(baseURL, "/"), c: c}
}

// Handover tells inventory to take the order's stock out of the warehouse. The caller's bearer token is
// forwarded, so inventory checks role and warehouse scope itself; warehouseID is the shipment's warehouse.
// Inventory treats a repeat as already done, which makes the call safe to retry.
func (c *Client) Handover(ctx context.Context, bearer, orderID, warehouseID string) error {
	body := map[string]string{"orderId": orderID, "warehouseId": warehouseID}
	_, err := c.c.Do(ctx, http.MethodPost, c.base+"/api/v1/inventory/shipments/handover",
		svcclient.Options{Bearer: bearer, IdempotencyKey: "handover-" + orderID}, body, nil)
	var apiErr *svcclient.APIError
	if errors.As(err, &apiErr) && apiErr.Status < http.StatusInternalServerError {
		if apiErr.Status == http.StatusForbidden || apiErr.Status == http.StatusUnauthorized {
			return ErrForbidden
		}
		return ErrRejected
	}
	return err
}

// ReleaseCommitted tells inventory the order's parcel is stopped for a cancellation: the committed stock goes back
// to available. It competes with Handover under inventory's reservation lock, so only one of them can succeed;
// ErrStockLeft means the handover won. The caller's bearer token is forwarded; a repeat is safe.
func (c *Client) ReleaseCommitted(ctx context.Context, bearer, orderID, warehouseID string) error {
	body := map[string]string{"orderId": orderID, "warehouseId": warehouseID}
	_, err := c.c.Do(ctx, http.MethodPost, c.base+"/api/v1/inventory/shipments/stop",
		svcclient.Options{Bearer: bearer, IdempotencyKey: "stop-" + orderID}, body, nil)
	var apiErr *svcclient.APIError
	if errors.As(err, &apiErr) && apiErr.Status < http.StatusInternalServerError {
		switch apiErr.Status {
		case http.StatusForbidden, http.StatusUnauthorized:
			return ErrForbidden
		case http.StatusConflict:
			if apiErr.Code == "RELEASE_COMMITTED_FAILED" {
				return ErrStockLeft
			}
		}
		return ErrRejected
	}
	return err
}
