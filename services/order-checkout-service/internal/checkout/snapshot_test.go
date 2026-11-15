package checkout

import (
	"context"
	"errors"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/orders"
)

func TestPlacingAnOrderFreezesTheCommercialSnapshot_I04_SC07(t *testing.T) {
	f := newFixture()
	o := placed(t, f)
	snap, found, err := f.snaps.Get(context.Background(), o.OrderID)
	if err != nil || !found {
		t.Fatalf("snapshot = found=%v err=%v", found, err)
	}
	if snap.UserID != "user-lan" || snap.QuoteID != "Q-1" || snap.VoucherCode != "WELCOME10" || snap.TotalPayableVND != 660000 ||
		snap.SubtotalVND != 700000 || snap.DiscountVND != 70000 || snap.ShippingFeeVND != 30000 || len(snap.Lines) != 2 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if l := snap.Lines[0]; l.SkuName != "Backpack" || l.UnitPriceVND != 300000 || l.AllocatedUnitPaidVND != 270000 || l.Quantity != 2 {
		t.Fatalf("line = %+v", l)
	}
	if snap.ShippingAddress.District != "District 1" || snap.ShippingAddress.Recipient != "Nguyen Van Lan" || snap.CreatedAt.IsZero() {
		t.Fatalf("address/time = %+v", snap)
	}
}

func TestAReplayDoesNotReadTheAddressBookOrRewriteTheSnapshot(t *testing.T) {
	f := newFixture()
	placed(t, f)
	if _, replayed, err := place(t, f, lan, "key-1", "Q-1"); err != nil || !replayed {
		t.Fatalf("replay = %v %v", replayed, err)
	}
	if f.addrs.calls != 1 {
		t.Fatalf("address reads = %d, want 1", f.addrs.calls)
	}
}

func TestAnUnknownOrUnavailableAddressCreatesNoOrder(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want func(error) bool
	}{
		"gone":     {ErrAddressNotFound, func(e error) bool { return errors.Is(e, ErrAddressNotFound) }},
		"IAM down": {dependency.Wrap(errors.New("iam down")), dependency.IsUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			f.addrs.err = tc.err
			_, _, err := place(t, f, lan, "key-1", "Q-1")
			if !tc.want(err) {
				t.Fatalf("err = %v", err)
			}
			if f.stock.reserves != 0 || f.payments.calls != 0 {
				t.Fatal("something was reserved for an order that could not be placed")
			}
			list, total, _ := f.orders.Query(context.Background(), orders.Filter{UserID: "user-lan"})
			if total != 0 || len(list) != 0 {
				t.Fatalf("orders = %+v", list)
			}
		})
	}
}

func TestASnapshotThatCannotBeStoredClosesTheOrderBeforeAnythingIsReserved(t *testing.T) {
	f := newFixture()
	f.svc.Snapshots = failingSnapshots{f.snaps}
	o, _, err := place(t, f, lan, "key-1", "Q-1")
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != "FAILED_CLOSED" || o.FailureCode != FailureDependencyUnavailable {
		t.Fatalf("order = %+v", o)
	}
	if f.stock.reserves != 0 || f.payments.calls != 0 {
		t.Fatalf("stock=%d payments=%d: nothing may be reserved without the snapshot", f.stock.reserves, f.payments.calls)
	}
}
