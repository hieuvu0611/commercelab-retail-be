package shipment

import (
	"fmt"
	"sync/atomic"
	"testing"
)

func TestMemoryStoreContract(t *testing.T) {
	var n atomic.Int64
	uniq := func(p string) string { return fmt.Sprintf("%s-%d", p, n.Add(1)) }
	store := NewMemoryStore()
	runStoreContract(t, store, uniq, store.EventTypes)
}
