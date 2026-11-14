package domain

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// MaxCategoryDepth is the deepest level of the category tree (decision D-01). Root categories have depth 1.
const MaxCategoryDepth = 3

const (
	maxCategoryNameLen = 120
	maxCategorySlugLen = 64
)

var (
	ErrCategoryNotFound      = errors.New("category not found")
	ErrCategoryConflict      = errors.New("category conflicts with an existing category or state")
	ErrCategoryDepthExceeded = errors.New("category tree depth would exceed the maximum of 3")
	ErrCategoryCycle         = errors.New("a category cannot be moved under itself or its own descendant")
	ErrCategoryHasChildren   = errors.New("category still has child categories")
	ErrCategoryHasProducts   = errors.New("category still has products")
	// ErrCategoryPathTaken is returned by a store when another category already owns the path.
	ErrCategoryPathTaken = errors.New("category path is already taken")
)

var categorySlugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidationError lists field-level problems of a request.
type ValidationError struct {
	Fields map[string][]string
}

func (e *ValidationError) Error() string { return "validation failed" }

// Category is a node of the catalog category tree. Path is materialized from the slugs of the ancestors
// (for example "/backpacks/city"), so a subtree is every category whose path starts with "<path>/".
type Category struct {
	ID        string    `json:"id" bson:"_id"`
	ParentID  string    `json:"parentId" bson:"parentId"`
	Name      string    `json:"name" bson:"name"`
	Slug      string    `json:"slug" bson:"slug"`
	Path      string    `json:"path" bson:"path"`
	Depth     int       `json:"depth" bson:"depth"`
	SortOrder int       `json:"sortOrder" bson:"sortOrder"`
	IsActive  bool      `json:"isActive" bson:"isActive"`
	CreatedAt time.Time `json:"createdAt" bson:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt" bson:"updatedAt"`
}

// CategoryStore is the persistence the category rules need.
type CategoryStore interface {
	// Insert stores a new category; it returns ErrCategoryPathTaken when the path exists.
	Insert(ctx context.Context, c Category) error
	// Get returns ErrCategoryNotFound for an unknown id.
	Get(ctx context.Context, id string) (Category, error)
	// Subtree returns the category at path and all its descendants.
	Subtree(ctx context.Context, path string) ([]Category, error)
	// List returns every category.
	List(ctx context.Context) ([]Category, error)
	// SaveAll replaces the stored categories with the same ids; it returns ErrCategoryPathTaken on a path clash.
	SaveAll(ctx context.Context, cats []Category) error
	CountChildren(ctx context.Context, id string) (int64, error)
	CountProducts(ctx context.Context, id string) (int64, error)
	Delete(ctx context.Context, id string) error
}

// CategoryService applies the category rules on top of a CategoryStore.
type CategoryService struct {
	store CategoryStore
	now   func() time.Time
	newID func() string
}

func NewCategoryService(store CategoryStore) *CategoryService {
	return &CategoryService{store: store, now: func() time.Time { return time.Now().UTC() }, newID: newCategoryID}
}

func newCategoryID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return "cat-" + hex.EncodeToString(b)
}

// CreateCategoryInput is the data to create a category. An empty ParentID creates a root category.
type CreateCategoryInput struct {
	ParentID  string
	Name      string
	Slug      string
	SortOrder int
}

func validateName(name string, fields map[string][]string) string {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		fields["name"] = append(fields["name"], "name is required")
	case len(name) > maxCategoryNameLen:
		fields["name"] = append(fields["name"], fmt.Sprintf("name must be at most %d characters", maxCategoryNameLen))
	}
	return name
}

func validateSlug(slug string, fields map[string][]string) {
	if len(slug) == 0 || len(slug) > maxCategorySlugLen || !categorySlugPattern.MatchString(slug) {
		fields["slug"] = append(fields["slug"], "slug must be 1-64 characters of lowercase letters, digits and single hyphens")
	}
}

func childPath(parentPath, slug string) string { return parentPath + "/" + slug }

func mapPathErr(err error) error {
	if errors.Is(err, ErrCategoryPathTaken) {
		return fmt.Errorf("%w: another category with the same slug exists under this parent", ErrCategoryConflict)
	}
	return err
}

// Get returns one category or ErrCategoryNotFound.
func (s *CategoryService) Get(ctx context.Context, id string) (Category, error) {
	return s.store.Get(ctx, id)
}

// List returns every category, including inactive ones.
func (s *CategoryService) List(ctx context.Context) ([]Category, error) {
	return s.store.List(ctx)
}

// Create adds a category under a parent (or at the root).
func (s *CategoryService) Create(ctx context.Context, in CreateCategoryInput) (Category, error) {
	fields := map[string][]string{}
	name := validateName(in.Name, fields)
	validateSlug(in.Slug, fields)
	if len(fields) > 0 {
		return Category{}, &ValidationError{Fields: fields}
	}
	parentPath, depth := "", 1
	if in.ParentID != "" {
		parent, err := s.store.Get(ctx, in.ParentID)
		if err != nil {
			return Category{}, err
		}
		if !parent.IsActive {
			return Category{}, fmt.Errorf("%w: parent category is inactive", ErrCategoryConflict)
		}
		if parent.Depth+1 > MaxCategoryDepth {
			return Category{}, ErrCategoryDepthExceeded
		}
		parentPath, depth = parent.Path, parent.Depth+1
	}
	now := s.now()
	c := Category{
		ID: s.newID(), ParentID: in.ParentID, Name: name, Slug: in.Slug, Path: childPath(parentPath, in.Slug),
		Depth: depth, SortOrder: in.SortOrder, IsActive: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.Insert(ctx, c); err != nil {
		return Category{}, mapPathErr(err)
	}
	return c, nil
}

// repath returns the subtree with the root moved from oldPath to newPath and every depth shifted by depthDelta.
func repath(subtree []Category, oldPath, newPath string, depthDelta int, at time.Time) []Category {
	out := make([]Category, len(subtree))
	for i, c := range subtree {
		c.Path = newPath + strings.TrimPrefix(c.Path, oldPath)
		c.Depth += depthDelta
		c.UpdatedAt = at
		out[i] = c
	}
	return out
}

func maxDepth(cats []Category) int {
	deepest := 0
	for _, c := range cats {
		if c.Depth > deepest {
			deepest = c.Depth
		}
	}
	return deepest
}

// CategoryPatch lists the fields to change; nil fields stay as they are.
type CategoryPatch struct {
	Name      *string
	Slug      *string
	SortOrder *int
	IsActive  *bool
}

// Rename changes the name and, when slug is not empty, the slug. A new slug re-computes the subtree paths.
func (s *CategoryService) Rename(ctx context.Context, id, name, slug string) (Category, error) {
	patch := CategoryPatch{Name: &name}
	if slug != "" {
		patch.Slug = &slug
	}
	return s.Update(ctx, id, patch)
}

// Update applies patch. A new slug re-computes the paths of the whole subtree; activating a category needs
// an active parent.
func (s *CategoryService) Update(ctx context.Context, id string, patch CategoryPatch) (Category, error) {
	fields := map[string][]string{}
	if patch.Name != nil {
		trimmed := validateName(*patch.Name, fields)
		patch.Name = &trimmed
	}
	if patch.Slug != nil {
		validateSlug(*patch.Slug, fields)
	}
	if len(fields) > 0 {
		return Category{}, &ValidationError{Fields: fields}
	}
	c, err := s.store.Get(ctx, id)
	if err != nil {
		return Category{}, err
	}
	if patch.IsActive != nil && *patch.IsActive && !c.IsActive && c.ParentID != "" {
		parent, err := s.store.Get(ctx, c.ParentID)
		if err != nil {
			return Category{}, err
		}
		if !parent.IsActive {
			return Category{}, fmt.Errorf("%w: parent category is inactive", ErrCategoryConflict)
		}
	}
	subtree, err := s.store.Subtree(ctx, c.Path)
	if err != nil {
		return Category{}, err
	}
	newPath := c.Path
	if patch.Slug != nil {
		newPath = c.Path[:len(c.Path)-len(c.Slug)] + *patch.Slug
	}
	updated := repath(subtree, c.Path, newPath, 0, s.now())
	for i := range updated {
		if updated[i].ID != c.ID {
			continue
		}
		if patch.Name != nil {
			updated[i].Name = *patch.Name
		}
		if patch.Slug != nil {
			updated[i].Slug = *patch.Slug
		}
		if patch.SortOrder != nil {
			updated[i].SortOrder = *patch.SortOrder
		}
		if patch.IsActive != nil {
			updated[i].IsActive = *patch.IsActive
		}
		c = updated[i]
	}
	if err := s.store.SaveAll(ctx, updated); err != nil {
		return Category{}, mapPathErr(err)
	}
	return c, nil
}

// Move re-parents a category together with its subtree. An empty newParentID moves it to the root.
func (s *CategoryService) Move(ctx context.Context, id, newParentID string) (Category, error) {
	c, err := s.store.Get(ctx, id)
	if err != nil {
		return Category{}, err
	}
	parentPath, parentDepth := "", 0
	if newParentID != "" {
		parent, err := s.store.Get(ctx, newParentID)
		if err != nil {
			return Category{}, err
		}
		if parent.ID == c.ID || strings.HasPrefix(parent.Path, c.Path+"/") {
			return Category{}, ErrCategoryCycle
		}
		parentPath, parentDepth = parent.Path, parent.Depth
	}
	subtree, err := s.store.Subtree(ctx, c.Path)
	if err != nil {
		return Category{}, err
	}
	delta := parentDepth + 1 - c.Depth
	if maxDepth(subtree)+delta > MaxCategoryDepth {
		return Category{}, ErrCategoryDepthExceeded
	}
	updated := repath(subtree, c.Path, childPath(parentPath, c.Slug), delta, s.now())
	for i := range updated {
		if updated[i].ID == c.ID {
			updated[i].ParentID = newParentID
			c = updated[i]
		}
	}
	if err := s.store.SaveAll(ctx, updated); err != nil {
		return Category{}, mapPathErr(err)
	}
	return c, nil
}

// Deactivate hides a category from the storefront without deleting it.
func (s *CategoryService) Deactivate(ctx context.Context, id string) (Category, error) {
	c, err := s.store.Get(ctx, id)
	if err != nil {
		return Category{}, err
	}
	c.IsActive, c.UpdatedAt = false, s.now()
	if err := s.store.SaveAll(ctx, []Category{c}); err != nil {
		return Category{}, err
	}
	return c, nil
}

// Delete removes a category that has neither child categories nor products.
func (s *CategoryService) Delete(ctx context.Context, id string) error {
	if _, err := s.store.Get(ctx, id); err != nil {
		return err
	}
	children, err := s.store.CountChildren(ctx, id)
	if err != nil {
		return err
	}
	if children > 0 {
		return ErrCategoryHasChildren
	}
	products, err := s.store.CountProducts(ctx, id)
	if err != nil {
		return err
	}
	if products > 0 {
		return ErrCategoryHasProducts
	}
	return s.store.Delete(ctx, id)
}
