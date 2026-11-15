//go:build integration

package repository

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
)

func TestPostgresAddressStoreFollowsTheAddressBookRules(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL_IAM")
	if dsn == "" {
		t.Skip("DATABASE_URL_IAM is not set; run `make test-integration`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	pool, err := db.ConnectPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)

	users := []string{"it-addr-a-" + time.Now().Format("150405.000000"), "it-addr-b-" + time.Now().Format("150405.000000")}
	for _, id := range users {
		if _, err := pool.Exec(ctx, `INSERT INTO iam.users (id, email, full_name, password_hash, role) VALUES ($1, $2, 'IT', 'x', 'CUSTOMER')`, id, id+"@example.com"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM iam.users WHERE id = $1`, id) })
	}
	store := NewAddressRepository(pool)
	runAddressStoreContract(t, store, users[0], users[1])

	// Concurrent creates that all ask to be the default leave exactly one default.
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := sampleAddress("Racer")
			in.IsDefault = true
			_, _ = store.Create(ctx, users[1], in)
		}()
	}
	wg.Wait()
	list, err := store.List(ctx, users[1])
	if err != nil {
		t.Fatal(err)
	}
	defaults := 0
	for _, a := range list {
		if a.IsDefault {
			defaults++
		}
	}
	if len(list) != 4 || defaults != 1 {
		t.Fatalf("concurrent creates: %d addresses, %d defaults", len(list), defaults)
	}
}
