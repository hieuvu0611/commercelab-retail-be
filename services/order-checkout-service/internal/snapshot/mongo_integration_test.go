//go:build integration

package snapshot

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
)

func TestMongoStoreContract(t *testing.T) {
	uri, name := os.Getenv("MONGO_URI"), os.Getenv("MONGO_DATABASE")
	if uri == "" {
		t.Skip("MONGO_URI is not set; run `make test-integration`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	mongoStore, _, err := db.ConnectMongo(ctx, uri, name)
	if err != nil {
		t.Fatalf("connect mongo: %v", err)
	}
	t.Cleanup(func() { _ = mongoStore.Close(context.Background()) })
	store, err := NewMongoStore(ctx, mongoStore)
	if err != nil {
		t.Fatal(err)
	}
	// NewMongoStore twice must not conflict with the index that exists already.
	if _, err := NewMongoStore(ctx, mongoStore); err != nil {
		t.Fatalf("second NewMongoStore: %v", err)
	}
	run := time.Now().UnixNano()
	var ids []string
	t.Cleanup(func() {
		for _, id := range ids {
			_, _ = mongoStore.Database().Collection(Collection).DeleteOne(context.Background(), map[string]any{"order_id": id})
		}
	})
	runContract(t, store, func() string {
		id := fmt.Sprintf("O-it-%d-%d", run, len(ids))
		ids = append(ids, id)
		return id
	})
}

type staticErasureChecker struct {
	erased map[string]time.Time
}

func (c staticErasureChecker) IsErased(_ context.Context, userID string) (bool, time.Time, error) {
	ts, ok := c.erased[userID]
	return ok, ts, nil
}

func TestMongoStoreRedactUserPIIPreservesCommercialDataAndBlocksReplay(t *testing.T) {
	uri, name := os.Getenv("MONGO_URI"), os.Getenv("MONGO_DATABASE")
	if uri == "" {
		t.Skip("MONGO_URI is not set; run `make test-integration`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	mongoStore, _, err := db.ConnectMongo(ctx, uri, name)
	if err != nil {
		t.Fatalf("connect mongo: %v", err)
	}
	t.Cleanup(func() { _ = mongoStore.Close(context.Background()) })
	rawStore, err := NewMongoStore(ctx, mongoStore)
	if err != nil {
		t.Fatal(err)
	}

	run := time.Now().UnixNano()
	userID := fmt.Sprintf("usr-pii-%d", run)
	existingOrderID := fmt.Sprintf("O-pii-existing-%d", run)
	replayedOrderID := fmt.Sprintf("O-pii-replay-%d", run)
	t.Cleanup(func() {
		_, _ = mongoStore.Database().Collection(Collection).DeleteMany(context.Background(), map[string]any{"user_id": userID})
	})

	orig := Snapshot{
		OrderID: existingOrderID,
		UserID:  userID,
		QuoteID: "Q-PII-1",
		Lines: []Line{{
			SkuID:                "SKU-BALO-BLACK-20L",
			SkuName:              "City Backpack",
			Quantity:             2,
			UnitPriceVND:         300000,
			LineDiscountVND:      60000,
			AllocatedUnitPaidVND: 270000,
		}},
		SubtotalVND:     600000,
		DiscountVND:     60000,
		ShippingFeeVND:  30000,
		TotalPayableVND: 570000,
		ShippingAddress: Address{
			Recipient: "Lan Nguyen",
			Phone:     "0901234567",
			Line1:     "123 Le Loi",
			Ward:      "Ben Nghe",
			District:  "District 1",
			Province:  "Ho Chi Minh",
		},
		CreatedAt: time.Now().UTC(),
	}
	if err := rawStore.Save(ctx, orig); err != nil {
		t.Fatalf("save original snapshot: %v", err)
	}

	erasedAt := time.Now().UTC().Truncate(time.Second)
	modified, err := rawStore.RedactUserPII(ctx, userID, erasedAt)
	if err != nil {
		t.Fatalf("redact user PII: %v", err)
	}
	if modified != 1 {
		t.Fatalf("modified = %d, want 1", modified)
	}

	got, found, err := rawStore.Get(ctx, existingOrderID)
	if err != nil || !found {
		t.Fatalf("get redacted snapshot: found=%v err=%v", found, err)
	}
	if !got.PIIRedacted || got.ShippingAddress != RedactedAddress() {
		t.Fatalf("expected redacted shipping address, got %+v", got)
	}
	if got.TotalPayableVND != 570000 || len(got.Lines) != 1 || got.Lines[0].AllocatedUnitPaidVND != 270000 {
		t.Fatalf("commercial data must remain intact for 10-year retention: %+v", got)
	}

	guarded := WithErasureTombstones(rawStore, staticErasureChecker{erased: map[string]time.Time{userID: erasedAt}})
	replayed := orig
	replayed.OrderID = replayedOrderID
	if err := guarded.Save(ctx, replayed); err != nil {
		t.Fatalf("save replayed snapshot: %v", err)
	}
	gotReplay, found, err := rawStore.Get(ctx, replayedOrderID)
	if err != nil || !found {
		t.Fatalf("get replayed snapshot: found=%v err=%v", found, err)
	}
	if !gotReplay.PIIRedacted || gotReplay.ShippingAddress != RedactedAddress() {
		t.Fatalf("replayed snapshot resurrected PII: %+v", gotReplay)
	}
}
