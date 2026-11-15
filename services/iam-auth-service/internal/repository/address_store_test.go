package repository

import (
	"context"
	"errors"
	"testing"
)

func sampleAddress(recipient string) AddressInput {
	return AddressInput{Recipient: recipient, Phone: "+84901234567", Line1: "12 Nguyen Hue", Ward: "Ben Nghe", District: "District 1", Province: "Ho Chi Minh City"}
}

func TestMemoryAddressStoreFollowsTheAddressBookRules(t *testing.T) {
	runAddressStoreContract(t, NewMemoryAddressStore(), "user-a", "user-b")
}

// runAddressStoreContract is shared with the Postgres integration test so both stores behave the same.
func runAddressStoreContract(t *testing.T, store AddressStore, userA, userB string) {
	t.Helper()
	ctx := context.Background()

	first, err := store.Create(ctx, userA, sampleAddress("First"))
	if err != nil || !first.IsDefault {
		t.Fatalf("the first address must be the default: %+v, %v", first, err)
	}
	second, err := store.Create(ctx, userA, sampleAddress("Second"))
	if err != nil || second.IsDefault {
		t.Fatalf("a later address is not the default: %+v, %v", second, err)
	}
	in := sampleAddress("Third")
	in.IsDefault = true
	third, err := store.Create(ctx, userA, in)
	if err != nil || !third.IsDefault {
		t.Fatalf("an explicit default must win: %+v, %v", third, err)
	}
	list, _ := store.List(ctx, userA)
	if len(list) != 3 || list[0].ID != third.ID || list[1].ID != second.ID || list[2].ID != first.ID {
		t.Fatalf("order must be default first, then newest: %+v", list)
	}
	if list[1].IsDefault || list[2].IsDefault {
		t.Fatalf("only one default allowed: %+v", list)
	}

	// Another user's address is invisible and cannot be changed or deleted.
	if _, err := store.Get(ctx, userB, first.ID); !errors.Is(err, ErrAddressNotFound) {
		t.Fatalf("get across users: %v", err)
	}
	if _, err := store.Update(ctx, userB, first.ID, sampleAddress("Hijack")); !errors.Is(err, ErrAddressNotFound) {
		t.Fatalf("update across users: %v", err)
	}
	if err := store.Delete(ctx, userB, first.ID); !errors.Is(err, ErrAddressNotFound) {
		t.Fatalf("delete across users: %v", err)
	}
	if list, _ := store.List(ctx, userB); len(list) != 0 {
		t.Fatalf("user B's book must be empty: %+v", list)
	}

	// Update keeps a default a default, and promotes another address when asked.
	edit := sampleAddress("Edited")
	updated, err := store.Update(ctx, userA, third.ID, edit)
	if err != nil || !updated.IsDefault || updated.Recipient != "Edited" {
		t.Fatalf("editing the default keeps it default: %+v, %v", updated, err)
	}
	edit.IsDefault = true
	if updated, err = store.Update(ctx, userA, first.ID, edit); err != nil || !updated.IsDefault {
		t.Fatalf("promote: %+v, %v", updated, err)
	}
	if got, _ := store.Get(ctx, userA, third.ID); got.IsDefault {
		t.Fatalf("the old default must be cleared: %+v", got)
	}

	// Deleting the default promotes the newest remaining address.
	if err := store.Delete(ctx, userA, first.ID); err != nil {
		t.Fatal(err)
	}
	list, _ = store.List(ctx, userA)
	if len(list) != 2 || list[0].ID != third.ID || !list[0].IsDefault {
		t.Fatalf("newest remaining must become default: %+v", list)
	}
	if err := store.Delete(ctx, userA, first.ID); !errors.Is(err, ErrAddressNotFound) {
		t.Fatalf("second delete: %v", err)
	}

	// The book is capped.
	for len(list) < MaxAddresses {
		if _, err := store.Create(ctx, userA, sampleAddress("Filler")); err != nil {
			t.Fatal(err)
		}
		list, _ = store.List(ctx, userA)
	}
	if _, err := store.Create(ctx, userA, sampleAddress("Eleventh")); !errors.Is(err, ErrAddressLimit) {
		t.Fatalf("11th address: %v", err)
	}
}
