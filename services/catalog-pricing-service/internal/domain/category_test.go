package domain

import (
	"context"
	"errors"
	"testing"
	"time"
)

func newTestCategoryService() (*CategoryService, *MemoryCategoryStore) {
	store := NewMemoryCategoryStore()
	n := 0
	svc := NewCategoryService(store)
	svc.newID = func() string { n++; return "cat-" + string(rune('a'+n-1)) }
	svc.now = func() time.Time { return time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC) }
	return svc, store
}

func mustCreate(t *testing.T, svc *CategoryService, parentID, slug string) Category {
	t.Helper()
	c, err := svc.Create(context.Background(), CreateCategoryInput{ParentID: parentID, Name: "Name " + slug, Slug: slug})
	if err != nil {
		t.Fatalf("create %s: %v", slug, err)
	}
	return c
}

func TestCreateBuildsMaterializedPathAndDepth(t *testing.T) {
	svc, _ := newTestCategoryService()
	root := mustCreate(t, svc, "", "backpacks")
	child := mustCreate(t, svc, root.ID, "city")
	leaf := mustCreate(t, svc, child.ID, "mini")

	if root.Path != "/backpacks" || root.Depth != 1 || !root.IsActive {
		t.Fatalf("root = %+v", root)
	}
	if child.Path != "/backpacks/city" || child.Depth != 2 || child.ParentID != root.ID {
		t.Fatalf("child = %+v", child)
	}
	if leaf.Path != "/backpacks/city/mini" || leaf.Depth != 3 {
		t.Fatalf("leaf = %+v", leaf)
	}
}

func TestCreateRules(t *testing.T) {
	tests := []struct {
		name string
		do   func(svc *CategoryService) error
		want error
	}{
		{"depth above 3 rejected", func(svc *CategoryService) error {
			a := mustCreate(t, svc, "", "a")
			b := mustCreate(t, svc, a.ID, "b")
			c := mustCreate(t, svc, b.ID, "c")
			_, err := svc.Create(context.Background(), CreateCategoryInput{ParentID: c.ID, Name: "D", Slug: "d"})
			return err
		}, ErrCategoryDepthExceeded},
		{"duplicate sibling slug rejected", func(svc *CategoryService) error {
			mustCreate(t, svc, "", "bags")
			_, err := svc.Create(context.Background(), CreateCategoryInput{Name: "Other", Slug: "bags"})
			return err
		}, ErrCategoryConflict},
		{"same slug under different parents allowed", func(svc *CategoryService) error {
			a := mustCreate(t, svc, "", "a")
			b := mustCreate(t, svc, "", "b")
			mustCreate(t, svc, a.ID, "x")
			_, err := svc.Create(context.Background(), CreateCategoryInput{ParentID: b.ID, Name: "X", Slug: "x"})
			return err
		}, nil},
		{"unknown parent rejected", func(svc *CategoryService) error {
			_, err := svc.Create(context.Background(), CreateCategoryInput{ParentID: "nope", Name: "X", Slug: "x"})
			return err
		}, ErrCategoryNotFound},
		{"inactive parent rejected", func(svc *CategoryService) error {
			a := mustCreate(t, svc, "", "a")
			if _, err := svc.Deactivate(context.Background(), a.ID); err != nil {
				return err
			}
			_, err := svc.Create(context.Background(), CreateCategoryInput{ParentID: a.ID, Name: "X", Slug: "x"})
			return err
		}, ErrCategoryConflict},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newTestCategoryService()
			if err := tc.do(svc); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestCreateValidatesFields(t *testing.T) {
	tests := []struct {
		name, catName, slug, field string
	}{
		{"empty name", "", "ok", "name"},
		{"blank name", "   ", "ok", "name"},
		{"empty slug", "Ok", "", "slug"},
		{"uppercase slug", "Ok", "Bags", "slug"},
		{"slug with slash", "Ok", "a/b", "slug"},
		{"slug with spaces", "Ok", "a b", "slug"},
		{"slug with double dash", "Ok", "a--b", "slug"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newTestCategoryService()
			_, err := svc.Create(context.Background(), CreateCategoryInput{Name: tc.catName, Slug: tc.slug})
			var ve *ValidationError
			if !errors.As(err, &ve) || len(ve.Fields[tc.field]) == 0 {
				t.Fatalf("err = %v, want validation error on %s", err, tc.field)
			}
		})
	}
}

func TestRenameChangesSubtreePathsWhenSlugChanges(t *testing.T) {
	svc, store := newTestCategoryService()
	root := mustCreate(t, svc, "", "backpacks")
	child := mustCreate(t, svc, root.ID, "city")

	renamed, err := svc.Rename(context.Background(), root.ID, "Bags", "bags")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "Bags" || renamed.Path != "/bags" {
		t.Fatalf("renamed = %+v", renamed)
	}
	got, _ := store.Get(context.Background(), child.ID)
	if got.Path != "/bags/city" {
		t.Fatalf("child path = %q", got.Path)
	}

	// Keeping the slug keeps every path.
	again, err := svc.Rename(context.Background(), root.ID, "Bags and more", "")
	if err != nil || again.Path != "/bags" || again.Name != "Bags and more" {
		t.Fatalf("again = %+v, %v", again, err)
	}
}

func TestRenameRejectsSiblingSlugConflict(t *testing.T) {
	svc, _ := newTestCategoryService()
	mustCreate(t, svc, "", "bags")
	other := mustCreate(t, svc, "", "bottles")
	if _, err := svc.Rename(context.Background(), other.ID, "Bottles", "bags"); !errors.Is(err, ErrCategoryConflict) {
		t.Fatalf("err = %v", err)
	}
}

func TestMoveRecomputesSubtreePaths(t *testing.T) {
	svc, store := newTestCategoryService()
	a := mustCreate(t, svc, "", "a")
	b := mustCreate(t, svc, "", "b")
	x := mustCreate(t, svc, a.ID, "x")
	y := mustCreate(t, svc, x.ID, "y")

	moved, err := svc.Move(context.Background(), x.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if moved.Path != "/b/x" || moved.Depth != 2 || moved.ParentID != b.ID {
		t.Fatalf("moved = %+v", moved)
	}
	gotY, _ := store.Get(context.Background(), y.ID)
	if gotY.Path != "/b/x/y" || gotY.Depth != 3 {
		t.Fatalf("y = %+v", gotY)
	}

	toRoot, err := svc.Move(context.Background(), x.ID, "")
	if err != nil || toRoot.Path != "/x" || toRoot.Depth != 1 {
		t.Fatalf("toRoot = %+v, %v", toRoot, err)
	}
	gotY, _ = store.Get(context.Background(), y.ID)
	if gotY.Path != "/x/y" || gotY.Depth != 2 {
		t.Fatalf("y after root move = %+v", gotY)
	}
}

func TestMoveRules(t *testing.T) {
	tests := []struct {
		name string
		do   func(svc *CategoryService) error
		want error
	}{
		{"under itself rejected", func(svc *CategoryService) error {
			a := mustCreate(t, svc, "", "a")
			_, err := svc.Move(context.Background(), a.ID, a.ID)
			return err
		}, ErrCategoryCycle},
		{"under its own child rejected", func(svc *CategoryService) error {
			a := mustCreate(t, svc, "", "a")
			b := mustCreate(t, svc, a.ID, "b")
			_, err := svc.Move(context.Background(), a.ID, b.ID)
			return err
		}, ErrCategoryCycle},
		{"under its own grandchild rejected", func(svc *CategoryService) error {
			a := mustCreate(t, svc, "", "a")
			b := mustCreate(t, svc, a.ID, "b")
			c := mustCreate(t, svc, b.ID, "c")
			_, err := svc.Move(context.Background(), a.ID, c.ID)
			return err
		}, ErrCategoryCycle},
		{"subtree that would exceed depth 3 rejected", func(svc *CategoryService) error {
			a := mustCreate(t, svc, "", "a")
			b := mustCreate(t, svc, a.ID, "b")
			mustCreate(t, svc, b.ID, "c")
			z := mustCreate(t, svc, "", "z")
			zz := mustCreate(t, svc, z.ID, "zz")
			_, err := svc.Move(context.Background(), a.ID, zz.ID)
			return err
		}, ErrCategoryDepthExceeded},
		{"sibling slug conflict at destination rejected", func(svc *CategoryService) error {
			a := mustCreate(t, svc, "", "a")
			mustCreate(t, svc, a.ID, "x")
			b := mustCreate(t, svc, "", "b")
			x2 := mustCreate(t, svc, b.ID, "x")
			_, err := svc.Move(context.Background(), x2.ID, a.ID)
			return err
		}, ErrCategoryConflict},
		{"unknown destination rejected", func(svc *CategoryService) error {
			a := mustCreate(t, svc, "", "a")
			_, err := svc.Move(context.Background(), a.ID, "nope")
			return err
		}, ErrCategoryNotFound},
		{"unknown category rejected", func(svc *CategoryService) error {
			_, err := svc.Move(context.Background(), "nope", "")
			return err
		}, ErrCategoryNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newTestCategoryService()
			if err := tc.do(svc); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestMoveDoesNotConfuseSiblingPrefixes(t *testing.T) {
	// "/bag" must not be treated as the ancestor of "/bags".
	svc, _ := newTestCategoryService()
	bag := mustCreate(t, svc, "", "bag")
	bags := mustCreate(t, svc, "", "bags")
	moved, err := svc.Move(context.Background(), bag.ID, bags.ID)
	if err != nil || moved.Path != "/bags/bag" {
		t.Fatalf("moved = %+v, %v", moved, err)
	}
}

func TestDeactivate(t *testing.T) {
	svc, _ := newTestCategoryService()
	a := mustCreate(t, svc, "", "a")
	got, err := svc.Deactivate(context.Background(), a.ID)
	if err != nil || got.IsActive {
		t.Fatalf("got = %+v, %v", got, err)
	}
	if _, err := svc.Deactivate(context.Background(), "nope"); !errors.Is(err, ErrCategoryNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestDeleteOnlyWithoutChildrenAndProducts(t *testing.T) {
	svc, store := newTestCategoryService()
	a := mustCreate(t, svc, "", "a")
	b := mustCreate(t, svc, a.ID, "b")

	if err := svc.Delete(context.Background(), a.ID); !errors.Is(err, ErrCategoryHasChildren) {
		t.Fatalf("with children: %v", err)
	}
	store.SetProductCount(b.ID, 2)
	if err := svc.Delete(context.Background(), b.ID); !errors.Is(err, ErrCategoryHasProducts) {
		t.Fatalf("with products: %v", err)
	}
	store.SetProductCount(b.ID, 0)
	if err := svc.Delete(context.Background(), b.ID); err != nil {
		t.Fatalf("delete leaf: %v", err)
	}
	if err := svc.Delete(context.Background(), a.ID); err != nil {
		t.Fatalf("delete parent after child gone: %v", err)
	}
	if err := svc.Delete(context.Background(), a.ID); !errors.Is(err, ErrCategoryNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}
