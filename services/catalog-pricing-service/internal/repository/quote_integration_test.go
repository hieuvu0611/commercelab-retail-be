//go:build integration

package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/domain"
)

func TestQuoteRoundTripKeepsAddressVersionAndDiscountPct(t *testing.T) {
	dsn := integrationEnv(t, "DATABASE_URL_PRICING")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	pool, err := db.ConnectPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	repo := NewCatalogPricingRepository(pool, nil)

	quote, err := domain.CalculateQuote("Q-IT-"+time.Now().Format("150405.000000"), "user-it", "WELCOME10", 10, []domain.QuoteLineInput{
		{SkuID: "SKU-A", SkuName: "A", Quantity: 2, UnitPriceVND: 300000},
		{SkuID: "SKU-B", SkuName: "B", Quantity: 1, UnitPriceVND: 100000},
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	quote.AddressID = "addr-it"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM pricing.quotes WHERE quote_id = $1`, quote.QuoteID)
	})
	if err := repo.SaveQuote(ctx, quote); err != nil {
		t.Fatal(err)
	}

	got, err := repo.GetQuoteByID(ctx, quote.QuoteID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalPayableVND != 660000 || got.AddressID != "addr-it" || got.DiscountPct != 10 || got.Version != quote.Version || got.Version == "" {
		t.Fatalf("read back = %+v", got)
	}
	if len(got.Lines) != 2 || got.Lines[0].LineDiscountVND+got.Lines[1].LineDiscountVND != 70000 {
		t.Fatalf("line snapshot = %+v", got.Lines)
	}
	if !got.ExpiresAt.Truncate(time.Second).Equal(quote.ExpiresAt.Truncate(time.Second)) {
		t.Fatalf("expiresAt %v != %v", got.ExpiresAt, quote.ExpiresAt)
	}
	if _, err := repo.GetQuoteByID(ctx, "Q-IT-MISSING"); !errors.Is(err, ErrQuoteNotFound) {
		t.Fatalf("missing quote: %v", err)
	}
}
