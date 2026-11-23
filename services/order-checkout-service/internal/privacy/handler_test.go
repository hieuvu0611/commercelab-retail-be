package privacy

import (
	"context"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/snapshot"
)

func sampleSnapshot(orderID, userID string) snapshot.Snapshot {
	return snapshot.Snapshot{
		OrderID:     orderID,
		UserID:      userID,
		QuoteID:     "Q-100",
		VoucherCode: "WELCOME10",
		Lines: []snapshot.Line{
			{
				SkuID:                "SKU-BALO-BLACK-20L",
				SkuName:              "Backpack 20L",
				Quantity:             2,
				UnitPriceVND:         350000,
				LineDiscountVND:      70000,
				AllocatedUnitPaidVND: 315000,
			},
		},
		SubtotalVND:     700000,
		DiscountVND:     70000,
		ShippingFeeVND:  30000,
		TotalPayableVND: 660000,
		ShippingAddress: snapshot.Address{
			Recipient: "Nguyen Van Lan",
			Phone:     "0901234567",
			Line1:     "1 Le Loi",
			Ward:      "Ben Nghe",
			District:  "District 1",
			Province:  "Ho Chi Minh",
		},
		CreatedAt: time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC),
	}
}

func TestErasureRedactsExistingSnapshotsAndPreservesFinancialData(t *testing.T) {
	ctx := context.Background()
	tombstones := NewMemoryStore()
	rawStore := snapshot.NewMemoryStore()
	wrapped := snapshot.WithErasureTombstones(rawStore, tombstones)

	if err := wrapped.Save(ctx, sampleSnapshot("O-1", "user-lan")); err != nil {
		t.Fatalf("save target snapshot: %v", err)
	}
	if err := wrapped.Save(ctx, sampleSnapshot("O-2", "user-other")); err != nil {
		t.Fatalf("save unrelated snapshot: %v", err)
	}

	erasedAt := time.Date(2026, 11, 20, 12, 30, 0, 0, time.UTC)
	retentionUntil := erasedAt.AddDate(10, 0, 0)
	h := &Handler{Tombstones: tombstones, Snapshots: wrapped}

	ev := events.DomainEvent{
		EventID:       "evt-erase-1",
		EventType:     events.EventUserPIIErased,
		AggregateType: "User",
		AggregateID:   "user-lan",
		OccurredAt:    erasedAt,
		Payload: map[string]any{
			"userId":         "user-lan",
			"erasedAt":       erasedAt.Format(time.RFC3339),
			"retentionUntil": retentionUntil.Format(time.RFC3339),
		},
	}

	if err := h.Handle(ctx, ev); err != nil {
		t.Fatalf("handle erasure event: %v", err)
	}

	got, found, err := wrapped.Get(ctx, "O-1")
	if err != nil || !found {
		t.Fatalf("get O-1: found=%v err=%v", found, err)
	}
	if got.ShippingAddress != snapshot.RedactedAddress() {
		t.Fatalf("shipping address not redacted: %+v", got.ShippingAddress)
	}
	if !got.PIIRedacted || got.PIIRedactedAt == nil || !got.PIIRedactedAt.Equal(erasedAt) {
		t.Fatalf("pii redaction metadata mismatch: redacted=%v at=%v", got.PIIRedacted, got.PIIRedactedAt)
	}
	if len(got.Lines) != 1 || got.Lines[0].SkuID != "SKU-BALO-BLACK-20L" || got.Lines[0].AllocatedUnitPaidVND != 315000 ||
		got.SubtotalVND != 700000 || got.DiscountVND != 70000 || got.ShippingFeeVND != 30000 || got.TotalPayableVND != 660000 ||
		got.VoucherCode != "WELCOME10" || got.QuoteID != "Q-100" {
		t.Fatalf("commercial fields altered: %+v", got)
	}

	other, found, err := wrapped.Get(ctx, "O-2")
	if err != nil || !found {
		t.Fatalf("get O-2: found=%v err=%v", found, err)
	}
	if other.PIIRedacted || other.ShippingAddress.Recipient != "Nguyen Van Lan" {
		t.Fatalf("unrelated user snapshot was modified: %+v", other)
	}
}

func TestReplaySaveAfterErasureNeverResurrectsPII(t *testing.T) {
	ctx := context.Background()
	tombstones := NewMemoryStore()
	rawStore := snapshot.NewMemoryStore()
	wrapped := snapshot.WithErasureTombstones(rawStore, tombstones)

	erasedAt := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	h := &Handler{Tombstones: tombstones, Snapshots: wrapped}

	if err := h.Handle(ctx, events.DomainEvent{
		EventID:       "evt-erase-replay",
		EventType:     events.EventUserPIIErased,
		AggregateType: "User",
		AggregateID:   "user-lan",
		OccurredAt:    erasedAt,
		Payload: map[string]any{
			"userId":         "user-lan",
			"erasedAt":       erasedAt.Format(time.RFC3339),
			"retentionUntil": erasedAt.AddDate(10, 0, 0).Format(time.RFC3339),
		},
	}); err != nil {
		t.Fatalf("handle erasure event: %v", err)
	}

	if err := wrapped.Save(ctx, sampleSnapshot("O-REPLAY-1", "user-lan")); err != nil {
		t.Fatalf("replay save: %v", err)
	}

	stored, found, err := rawStore.Get(ctx, "O-REPLAY-1")
	if err != nil || !found {
		t.Fatalf("raw get O-REPLAY-1: found=%v err=%v", found, err)
	}
	if stored.ShippingAddress != snapshot.RedactedAddress() {
		t.Fatalf("raw PII leaked into storage on replay: %+v", stored.ShippingAddress)
	}
	if !stored.PIIRedacted || stored.PIIRedactedAt == nil || !stored.PIIRedactedAt.Equal(erasedAt) {
		t.Fatalf("expected pii_redacted metadata on replayed snapshot, got redacted=%v at=%v", stored.PIIRedacted, stored.PIIRedactedAt)
	}
	if stored.SubtotalVND != 700000 || stored.DiscountVND != 70000 || stored.TotalPayableVND != 660000 || len(stored.Lines) != 1 {
		t.Fatalf("commercial fields corrupted on replay save: %+v", stored)
	}
}

func TestGetRedactsUnredactedSnapshotWhenTombstoneExists(t *testing.T) {
	ctx := context.Background()
	tombstones := NewMemoryStore()
	rawStore := snapshot.NewMemoryStore()
	wrapped := snapshot.WithErasureTombstones(rawStore, tombstones)

	if err := rawStore.Save(ctx, sampleSnapshot("O-LEGACY", "user-lan")); err != nil {
		t.Fatalf("raw save: %v", err)
	}
	erasedAt := time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)
	if err := tombstones.Record(ctx, Tombstone{
		UserID:         "user-lan",
		ErasedAt:       erasedAt,
		RetentionUntil: erasedAt.AddDate(10, 0, 0),
	}); err != nil {
		t.Fatalf("record tombstone: %v", err)
	}

	got, found, err := wrapped.Get(ctx, "O-LEGACY")
	if err != nil || !found {
		t.Fatalf("get O-LEGACY: found=%v err=%v", found, err)
	}
	if got.ShippingAddress != snapshot.RedactedAddress() || !got.PIIRedacted {
		t.Fatalf("expected Get to redact snapshot with active tombstone: %+v", got)
	}
}

func TestDuplicateErasureEventsAreIdempotent(t *testing.T) {
	ctx := context.Background()
	tombstones := NewMemoryStore()
	rawStore := snapshot.NewMemoryStore()
	wrapped := snapshot.WithErasureTombstones(rawStore, tombstones)

	if err := wrapped.Save(ctx, sampleSnapshot("O-DUP", "user-lan")); err != nil {
		t.Fatalf("save: %v", err)
	}

	erasedAt := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	h := &Handler{Tombstones: tombstones, Snapshots: wrapped}
	ev := events.DomainEvent{
		EventID:       "evt-erase-dup",
		EventType:     events.EventUserPIIErased,
		AggregateType: "User",
		AggregateID:   "user-lan",
		OccurredAt:    erasedAt,
		Payload: map[string]any{
			"userId":         "user-lan",
			"erasedAt":       erasedAt.Format(time.RFC3339),
			"retentionUntil": erasedAt.AddDate(10, 0, 0).Format(time.RFC3339),
		},
	}

	if err := h.Handle(ctx, ev); err != nil {
		t.Fatalf("first erasure: %v", err)
	}
	if err := h.Handle(ctx, ev); err != nil {
		t.Fatalf("duplicate erasure: %v", err)
	}

	isErased, ts, err := tombstones.IsErased(ctx, "user-lan")
	if err != nil || !isErased || !ts.Equal(erasedAt) {
		t.Fatalf("tombstone state after duplicate = erased=%v ts=%v err=%v", isErased, ts, err)
	}
	got, found, err := wrapped.Get(ctx, "O-DUP")
	if err != nil || !found || got.ShippingAddress != snapshot.RedactedAddress() || got.TotalPayableVND != 660000 {
		t.Fatalf("snapshot after duplicate erasure = %+v found=%v err=%v", got, found, err)
	}
}
