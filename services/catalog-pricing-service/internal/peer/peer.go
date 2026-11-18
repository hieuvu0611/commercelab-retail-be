// Package peer calls the other services the quote depends on. Both calls forward the caller's own access
// token, so the callee verifies the same identity itself (no identity headers, no service account).
package peer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
)

const (
	callTimeout      = 3 * time.Second
	maxResponseBytes = 1 << 20
)

// Eligibility is the order service's answer to "can this customer use this voucher now".
type Eligibility struct {
	Code        string `json:"code"`
	Eligible    bool   `json:"eligible"`
	Reason      string `json:"reason"`
	DiscountPct int64  `json:"discountPct"`
}

type base struct {
	url  string
	http *http.Client
}

func newBase(baseURL string, client *http.Client) base {
	if client == nil {
		client = &http.Client{Timeout: callTimeout}
	}
	return base{url: strings.TrimRight(baseURL, "/"), http: client}
}

func (b base) get(ctx context.Context, path, bearer string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.url+path, nil)
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	res, err := b.http.Do(req)
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	return res, nil
}

// Vouchers reads voucher eligibility from the order service.
type Vouchers struct{ base }

func NewVouchers(orderURL string, client *http.Client) *Vouchers {
	return &Vouchers{newBase(orderURL, client)}
}

// Eligibility checks a voucher code for the owner of bearer. It reserves nothing.
func (v *Vouchers) Eligibility(ctx context.Context, bearer, code string) (Eligibility, error) {
	res, err := v.get(ctx, "/api/v1/vouchers/"+url.PathEscape(code)+"/eligibility", bearer)
	if err != nil {
		return Eligibility{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return Eligibility{}, dependency.Wrap(fmt.Errorf("voucher eligibility answered %d", res.StatusCode))
	}
	var body struct {
		Data Eligibility `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxResponseBytes)).Decode(&body); err != nil {
		return Eligibility{}, dependency.Wrap(fmt.Errorf("voucher eligibility answered malformed JSON: %w", err))
	}
	return body.Data, nil
}

type PromoEvalLine struct {
	SkuID        string `json:"skuId"`
	CategoryID   string `json:"categoryId,omitempty"`
	Quantity     int64  `json:"quantity"`
	UnitPriceVND int64  `json:"unitPriceVnd"`
}

type AppliedPromo struct {
	CampaignID  string `json:"campaignId"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	DiscountVND int64  `json:"discountVnd"`
}

type PromoEvaluation struct {
	TotalDiscountVND int64          `json:"totalDiscountVnd"`
	Applied          []AppliedPromo `json:"applied"`
}

// EvaluatePromotions asks order-checkout-service to evaluate eligible B08 campaigns (reserves nothing).
func (v *Vouchers) EvaluatePromotions(ctx context.Context, bearer string, campaignIDs []string, lines []PromoEvalLine) (PromoEvaluation, error) {
	payload, _ := json.Marshal(map[string]any{
		"campaignIds": campaignIDs,
		"lines":       lines,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.url+"/api/v1/promotions/evaluate", bytes.NewReader(payload))
	if err != nil {
		return PromoEvaluation{}, dependency.Wrap(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	res, err := v.http.Do(req)
	if err != nil {
		return PromoEvaluation{}, dependency.Wrap(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return PromoEvaluation{}, dependency.Wrap(fmt.Errorf("promotion evaluate answered %d", res.StatusCode))
	}
	var body struct {
		Data PromoEvaluation `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxResponseBytes)).Decode(&body); err != nil {
		return PromoEvaluation{}, dependency.Wrap(fmt.Errorf("promotion evaluate answered malformed JSON: %w", err))
	}
	return body.Data, nil
}

// Addresses checks delivery addresses with the IAM service.
type Addresses struct{ base }

func NewAddresses(iamURL string, client *http.Client) *Addresses {
	return &Addresses{newBase(iamURL, client)}
}

// Exists reports whether addressID is in the address book of the owner of bearer. The response body (personal
// data) is never read.
func (a *Addresses) Exists(ctx context.Context, bearer, addressID string) (bool, error) {
	res, err := a.get(ctx, "/api/v1/users/me/addresses/"+url.PathEscape(addressID), bearer)
	if err != nil {
		return false, err
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	}
	return false, dependency.Wrap(fmt.Errorf("address lookup answered %d", res.StatusCode))
}
