// Package orderclient tells the order service how a payment attempt ended.
package orderclient

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/svcclient"
	"github.com/hieuvu0611/commercelab-retail-be/services/payment-service/internal/attempt"
)

// Client calls the order service's internal payment-outcome route.
type Client struct {
	base   string
	client *svcclient.Client
}

func New(orderURL string, c *svcclient.Client) *Client {
	return &Client{base: strings.TrimRight(orderURL, "/"), client: c}
}

// PaymentOutcome reports the attempt's status and captured amount. The idempotency key names the attempt and the
// status, so a retried call (and the order service's own replay handling) never applies an outcome twice.
func (c *Client) PaymentOutcome(ctx context.Context, a attempt.Attempt) error {
	body := map[string]any{"attemptId": a.AttemptID, "status": a.Status, "capturedAmountVnd": a.CapturedAmountVND}
	_, err := c.client.Do(ctx, http.MethodPost, c.base+"/api/v1/orders/"+url.PathEscape(a.OrderID)+"/payment-outcome",
		svcclient.Options{Internal: true, IdempotencyKey: "outcome-" + a.AttemptID + "-" + a.Status}, body, nil)
	return err
}
