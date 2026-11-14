package domain

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// MemoryCategoryStore is an in-memory CategoryStore for tests and for running without MongoDB in development.
type MemoryCategoryStore struct {
	mu       sync.RWMutex
	cats     map[string]Category
	products map[string]int64
}

func NewMemoryCategoryStore() *MemoryCategoryStore {
	return &MemoryCategoryStore{cats: map[string]Category{}, products: map[string]int64{}}
}

// SetProductCount sets how many products reference the category (test helper).
func (m *MemoryCategoryStore) SetProductCount(id string, n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.products[id] = n
}

func (m *MemoryCategoryStore) pathOwner(path string) (string, bool) {
	for id, c := range m.cats {
		if c.Path == path {
			return id, true
		}
	}
	return "", false
}

func (m *MemoryCategoryStore) Insert(_ context.Context, c Category) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, taken := m.pathOwner(c.Path); taken {
		return ErrCategoryPathTaken
	}
	m.cats[c.ID] = c
	return nil
}

func (m *MemoryCategoryStore) Get(_ context.Context, id string) (Category, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.cats[id]
	if !ok {
		return Category{}, ErrCategoryNotFound
	}
	return c, nil
}

func (m *MemoryCategoryStore) Subtree(_ context.Context, path string) ([]Category, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Category
	for _, c := range m.cats {
		if c.Path == path || strings.HasPrefix(c.Path, path+"/") {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (m *MemoryCategoryStore) List(_ context.Context) ([]Category, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Category, 0, len(m.cats))
	for _, c := range m.cats {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (m *MemoryCategoryStore) SaveAll(_ context.Context, cats []Category) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := make(map[string]Category, len(m.cats))
	for id, c := range m.cats {
		next[id] = c
	}
	for _, c := range cats {
		next[c.ID] = c
	}
	seen := map[string]string{}
	for id, c := range next {
		if other, dup := seen[c.Path]; dup && other != id {
			return ErrCategoryPathTaken
		}
		seen[c.Path] = id
	}
	m.cats = next
	return nil
}

func (m *MemoryCategoryStore) CountChildren(_ context.Context, id string) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var n int64
	for _, c := range m.cats {
		if c.ParentID == id {
			n++
		}
	}
	return n, nil
}

func (m *MemoryCategoryStore) CountProducts(_ context.Context, id string) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.products[id], nil
}

func (m *MemoryCategoryStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cats, id)
	return nil
}
