package repository

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"sync"
	"time"
)

// MaxAddresses is the most addresses one customer may keep.
const MaxAddresses = 10

var (
	ErrAddressNotFound = errors.New("address not found")
	ErrAddressLimit    = errors.New("the address book holds at most 10 addresses")
)

// Address is one delivery address. Phone and the address lines are personal data and must never be logged.
type Address struct {
	ID        string
	UserID    string
	Recipient string
	Phone     string
	Line1     string
	Ward      string
	District  string
	Province  string
	IsDefault bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AddressInput is the editable part of an address. The input is already validated and normalised.
type AddressInput struct {
	Recipient string
	Phone     string
	Line1     string
	Ward      string
	District  string
	Province  string
	IsDefault bool
}

// AddressStore keeps the address books. Every operation is scoped to one user: another user's address id is
// reported as ErrAddressNotFound. The first address of a user is the default; making an address the default
// clears the previous one; deleting the default promotes the newest remaining address. An address that is the
// default stays the default on update (choose another address as the default instead).
type AddressStore interface {
	// List returns the default address first, then the others newest first.
	List(ctx context.Context, userID string) ([]Address, error)
	Get(ctx context.Context, userID, id string) (Address, error)
	Create(ctx context.Context, userID string, in AddressInput) (Address, error)
	Update(ctx context.Context, userID, id string, in AddressInput) (Address, error)
	Delete(ctx context.Context, userID, id string) error
}

func sortAddresses(list []Address) {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].IsDefault != list[j].IsDefault {
			return list[i].IsDefault
		}
		return list[i].CreatedAt.After(list[j].CreatedAt)
	})
}

// MemoryAddressStore is the in-memory AddressStore for tests.
type MemoryAddressStore struct {
	mu    sync.Mutex
	seq   int
	now   func() time.Time
	books map[string][]Address
}

func NewMemoryAddressStore() *MemoryAddressStore {
	start := time.Now().UTC()
	tick := 0
	return &MemoryAddressStore{books: map[string][]Address{}, now: func() time.Time {
		tick++ // strictly increasing, so "newest first" is deterministic
		return start.Add(time.Duration(tick) * time.Millisecond)
	}}
}

func (m *MemoryAddressStore) List(_ context.Context, userID string) ([]Address, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]Address{}, m.books[userID]...)
	sortAddresses(out)
	return out, nil
}

func (m *MemoryAddressStore) Get(_ context.Context, userID, id string) (Address, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.books[userID] {
		if a.ID == id {
			return a, nil
		}
	}
	return Address{}, ErrAddressNotFound
}

func (m *MemoryAddressStore) Create(_ context.Context, userID string, in AddressInput) (Address, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	book := m.books[userID]
	if len(book) >= MaxAddresses {
		return Address{}, ErrAddressLimit
	}
	m.seq++
	now := m.now()
	a := Address{
		ID: "addr-mem-" + strconv.Itoa(m.seq), UserID: userID, Recipient: in.Recipient, Phone: in.Phone,
		Line1: in.Line1, Ward: in.Ward, District: in.District, Province: in.Province,
		IsDefault: in.IsDefault || len(book) == 0, CreatedAt: now, UpdatedAt: now,
	}
	if a.IsDefault {
		clearDefault(book)
	}
	m.books[userID] = append(book, a)
	return a, nil
}

func clearDefault(book []Address) {
	for i := range book {
		book[i].IsDefault = false
	}
}

func (m *MemoryAddressStore) Update(_ context.Context, userID, id string, in AddressInput) (Address, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	book := m.books[userID]
	for i := range book {
		if book[i].ID != id {
			continue
		}
		makeDefault := in.IsDefault || book[i].IsDefault
		if makeDefault {
			clearDefault(book)
		}
		a := &book[i]
		a.Recipient, a.Phone, a.Line1, a.Ward, a.District, a.Province = in.Recipient, in.Phone, in.Line1, in.Ward, in.District, in.Province
		a.IsDefault, a.UpdatedAt = makeDefault, m.now()
		return *a, nil
	}
	return Address{}, ErrAddressNotFound
}

func (m *MemoryAddressStore) Delete(_ context.Context, userID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	book := m.books[userID]
	for i := range book {
		if book[i].ID != id {
			continue
		}
		wasDefault := book[i].IsDefault
		book = append(book[:i], book[i+1:]...)
		if wasDefault && len(book) > 0 {
			newest := 0
			for j := range book {
				if book[j].CreatedAt.After(book[newest].CreatedAt) {
					newest = j
				}
			}
			book[newest].IsDefault = true
		}
		m.books[userID] = book
		return nil
	}
	return ErrAddressNotFound
}
