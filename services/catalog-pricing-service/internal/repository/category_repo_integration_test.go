//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/domain"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// newMongoCategoryFixture connects to Mongo with a throw-away database that is dropped afterwards.
func newMongoCategoryFixture(t *testing.T) (*domain.CategoryService, *MongoCategoryStore, *db.MongoStore, context.Context) {
	t.Helper()
	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		t.Skip("MONGO_URI is not set; run `make test-integration`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	store, err := db.NewMongoStore(uri, fmt.Sprintf("it_catalog_%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("mongo store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Database().Drop(context.Background())
		_ = store.Close(context.Background())
	})
	cats, err := NewMongoCategoryStore(ctx, store)
	if err != nil {
		t.Fatalf("category store: %v", err)
	}
	return domain.NewCategoryService(cats), cats, store, ctx
}

func TestMongoCategoryTreeRules(t *testing.T) {
	svc, cats, store, ctx := newMongoCategoryFixture(t)

	root, err := svc.Create(ctx, domain.CreateCategoryInput{Name: "Backpacks", Slug: "backpacks"})
	if err != nil {
		t.Fatal(err)
	}
	city, err := svc.Create(ctx, domain.CreateCategoryInput{ParentID: root.ID, Name: "City", Slug: "city"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, domain.CreateCategoryInput{ParentID: root.ID, Name: "City again", Slug: "city"}); !errors.Is(err, domain.ErrCategoryConflict) {
		t.Fatalf("duplicate sibling: %v", err)
	}
	bottles, err := svc.Create(ctx, domain.CreateCategoryInput{Name: "Bottles", Slug: "bottles"})
	if err != nil {
		t.Fatal(err)
	}

	// Moving a subtree under its own child is rejected and nothing changes.
	if _, err := svc.Move(ctx, root.ID, city.ID); !errors.Is(err, domain.ErrCategoryCycle) {
		t.Fatalf("cycle: %v", err)
	}

	// Move recomputes the subtree paths in the database.
	if _, err := svc.Move(ctx, root.ID, bottles.ID); err != nil {
		t.Fatalf("move: %v", err)
	}
	got, err := cats.Get(ctx, city.ID)
	if err != nil || got.Path != "/bottles/backpacks/city" || got.Depth != 3 {
		t.Fatalf("city after move = %+v, %v", got, err)
	}

	// The unique index on path rejects a direct clash even when the service is bypassed.
	if err := cats.Insert(ctx, domain.Category{ID: "dup", Name: "x", Slug: "city", Path: got.Path, Depth: 3, IsActive: true}); !errors.Is(err, domain.ErrCategoryPathTaken) {
		t.Fatalf("index clash: %v", err)
	}

	// A regex-looking path must match literally: ".*" finds nothing.
	sub, err := cats.Subtree(ctx, "/.*")
	if err != nil || len(sub) != 0 {
		t.Fatalf("regex path matched %d categories, %v", len(sub), err)
	}
	sub, err = cats.Subtree(ctx, "/bottles")
	if err != nil || len(sub) != 3 {
		t.Fatalf("subtree = %d, %v", len(sub), err)
	}

	// Delete rules: children block, then products block, then it succeeds.
	if err := svc.Delete(ctx, bottles.ID); !errors.Is(err, domain.ErrCategoryHasChildren) {
		t.Fatalf("delete with children: %v", err)
	}
	if _, err := store.Database().Collection("catalog_products").InsertOne(ctx, bson.M{"_id": "p1", "primaryCategoryId": city.ID}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, city.ID); !errors.Is(err, domain.ErrCategoryHasProducts) {
		t.Fatalf("delete with products: %v", err)
	}
	if _, err := store.Database().Collection("catalog_products").DeleteOne(ctx, bson.M{"_id": "p1"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, city.ID); err != nil {
		t.Fatalf("delete leaf: %v", err)
	}
	if _, err := cats.Get(ctx, city.ID); !errors.Is(err, domain.ErrCategoryNotFound) {
		t.Fatalf("get deleted: %v", err)
	}
}
