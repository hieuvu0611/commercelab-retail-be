package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/services/ledger-reconciliation-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/ledger-reconciliation-service/internal/repository"
)

type POSHandler struct {
	Repo *repository.LedgerRepository
}

func (h *POSHandler) Handle(ctx context.Context, e events.DomainEvent) error {
	if e.EventType != events.EventPOSSaleCompleted {
		return nil
	}
	if h == nil || h.Repo == nil {
		return nil
	}
	orderID := stringField(e.Payload, "orderId")
	if orderID == "" {
		orderID = strings.TrimSpace(e.AggregateID)
	}
	paymentMethod := strings.ToUpper(stringField(e.Payload, "paymentMethod"))
	warehouseID := stringField(e.Payload, "warehouseId")
	cashierID := stringField(e.Payload, "cashierId")
	totalVND, ok := int64Field(e.Payload, "totalVnd")
	if orderID == "" || !ok || totalVND <= 0 {
		log.Printf("[ledger] skipping invalid pos.sale.completed event %s: orderId=%q totalVnd=%d", e.EventID, orderID, totalVND)
		return nil
	}

	entryID := "JE-POS-" + orderID
	entry, err := domain.NewPOSSaleCompletedEntry(entryID, orderID, warehouseID, paymentMethod, totalVND, cashierID)
	if err != nil {
		log.Printf("[ledger] skipping unsupported pos.sale.completed event %s: %v", e.EventID, err)
		return nil
	}
	if _, err := h.Repo.RecordJournalEntry(ctx, entry); err != nil {
		return fmt.Errorf("record POS ledger entry for %s: %w", orderID, err)
	}
	return nil
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func int64Field(m map[string]any, key string) (int64, bool) {
	if m == nil {
		return 0, false
	}
	v, ok := m[key]
	if !ok || v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case float64:
		return int64(n), true
	case json.Number:
		parsed, err := n.Int64()
		return parsed, err == nil
	default:
		return 0, false
	}
}
