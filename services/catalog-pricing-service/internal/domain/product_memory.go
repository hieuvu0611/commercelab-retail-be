package domain

import (
	"context"
	"slices"
	"sort"
	"strings"
	"sync"
)

// MemoryProductStore is an in-memory ProductStore for tests and for development without MongoDB.
type MemoryProductStore struct {
	mu       sync.Mutex
	products map[string]Product
	drafts   map[string]ProductRevision
	skus     map[string]SKU
}

func NewMemoryProductStore() *MemoryProductStore {
	return &MemoryProductStore{products: map[string]Product{}, drafts: map[string]ProductRevision{}, skus: map[string]SKU{}}
}

func (m *MemoryProductStore) slugTaken(slug, exceptID string) bool {
	for id, p := range m.products {
		if p.Slug == slug && id != exceptID {
			return true
		}
	}
	return false
}

func (m *MemoryProductStore) InsertProduct(_ context.Context, p Product) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.slugTaken(p.Slug, "") {
		return ErrProductSlugTaken
	}
	m.products[p.ID] = p
	return nil
}

func (m *MemoryProductStore) GetProduct(_ context.Context, id string) (Product, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.products[id]
	if !ok {
		return Product{}, ErrProductNotFound
	}
	return p, nil
}

func (m *MemoryProductStore) SaveProduct(_ context.Context, p Product, expectedVersion int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.products[p.ID]
	if !ok {
		return ErrProductNotFound
	}
	if cur.Version != expectedVersion {
		return ErrVersionConflict
	}
	if m.slugTaken(p.Slug, p.ID) {
		return ErrProductSlugTaken
	}
	m.products[p.ID] = p
	return nil
}

func (m *MemoryProductStore) ListProducts(_ context.Context, f ProductFilter) ([]Product, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q := strings.ToLower(f.Q)
	var matched []Product
	for _, p := range m.products {
		if f.Status != "" && p.Status != f.Status {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(p.Name), q) && !strings.Contains(p.Slug, q) {
			continue
		}
		matched = append(matched, p)
	}
	sort.Slice(matched, func(i, j int) bool {
		if !matched[i].CreatedAt.Equal(matched[j].CreatedAt) {
			return matched[i].CreatedAt.After(matched[j].CreatedAt)
		}
		return matched[i].ID < matched[j].ID
	})
	total := int64(len(matched))
	start := (f.Page - 1) * f.Limit
	if start > len(matched) {
		start = len(matched)
	}
	end := min(start+f.Limit, len(matched))
	return matched[start:end], total, nil
}

func (m *MemoryProductStore) GetDraft(_ context.Context, productID string) (ProductRevision, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.drafts[productID]
	return d, ok, nil
}

func (m *MemoryProductStore) SaveDraft(_ context.Context, d ProductRevision, expectedVersion int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, exists := m.drafts[d.ProductID]
	if expectedVersion == 0 && exists || expectedVersion != 0 && (!exists || cur.Product.Version != expectedVersion) {
		return ErrVersionConflict
	}
	m.drafts[d.ProductID] = d
	return nil
}

func (m *MemoryProductStore) ListSKUs(_ context.Context, productID string) ([]SKU, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []SKU
	for _, s := range m.skus {
		if s.ProductID == productID {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SkuID < out[j].SkuID })
	return out, nil
}

func (m *MemoryProductStore) GetSKU(_ context.Context, skuID string) (SKU, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.skus[skuID]
	if !ok {
		return SKU{}, ErrSKUNotFound
	}
	return s, nil
}

func (m *MemoryProductStore) InsertSKU(_ context.Context, s SKU) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.skus[s.SkuID]; ok {
		return ErrSKUCodeTaken
	}
	m.skus[s.SkuID] = s
	return nil
}

func (m *MemoryProductStore) SaveSKU(_ context.Context, s SKU) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.skus[s.SkuID]; !ok {
		return ErrSKUNotFound
	}
	m.skus[s.SkuID] = s
	return nil
}

func (m *MemoryProductStore) DeleteSKU(_ context.Context, skuID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.skus, skuID)
	return nil
}

func (m *MemoryProductStore) DeleteDraft(_ context.Context, productID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.drafts, productID)
	return nil
}

func wordsOf(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 127)
	})
}

// FindPublished implements StorefrontStore; the text matches whole words of the name, brand or description.
func (m *MemoryProductStore) FindPublished(_ context.Context, text string, categoryIDs []string) ([]PublishedRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	queryWords := wordsOf(text)
	var refs []PublishedRef
	for _, p := range m.products {
		if p.Status != ProductPublished {
			continue
		}
		if categoryIDs != nil && !slices.ContainsFunc(p.CategoryIDs, func(id string) bool { return slices.Contains(categoryIDs, id) }) {
			continue
		}
		if strings.TrimSpace(text) != "" {
			words := wordsOf(p.Name + " " + p.Brand + " " + p.Description)
			if !slices.ContainsFunc(queryWords, func(w string) bool { return slices.Contains(words, w) }) {
				continue
			}
		}
		ref := PublishedRef{ID: p.ID}
		if p.PublishedAt != nil {
			ref.PublishedAt = *p.PublishedAt
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// ActiveSKUs implements StorefrontStore.
func (m *MemoryProductStore) ActiveSKUs(_ context.Context, productIDs []string, attrs map[string]string) ([]SKU, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []SKU
	for _, s := range m.skus {
		if !s.IsActive || !slices.Contains(productIDs, s.ProductID) {
			continue
		}
		ok := true
		for axis, want := range attrs {
			if !strings.EqualFold(strings.TrimSpace(s.Attributes[axis]), strings.TrimSpace(want)) {
				ok = false
			}
		}
		if ok {
			out = append(out, s)
		}
	}
	return out, nil
}

// PublishedByIDs implements StorefrontStore.
func (m *MemoryProductStore) PublishedByIDs(_ context.Context, ids []string) ([]Product, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Product
	for _, id := range ids {
		if p, ok := m.products[id]; ok && p.Status == ProductPublished {
			out = append(out, p)
		}
	}
	return out, nil
}

// PublishedBySlug implements StorefrontStore.
func (m *MemoryProductStore) PublishedBySlug(_ context.Context, slug string) (Product, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.products {
		if p.Slug == slug && p.Status == ProductPublished {
			return p, nil
		}
	}
	return Product{}, ErrProductNotFound
}

var _ StorefrontStore = (*MemoryProductStore)(nil)
