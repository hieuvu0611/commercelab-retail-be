package proxy

import "testing"

func TestResolveTargetRoutesEveryPublicPrefixToItsService(t *testing.T) {
	targets := ServiceTargets{
		IAM: "iam", Catalog: "catalog", Inventory: "inventory", Order: "order", Payment: "payment",
		Fulfillment: "fulfillment", Ledger: "ledger", Backoffice: "backoffice", Notification: "notification",
	}
	router := NewRouter(targets)

	tests := []struct {
		path string
		want string
	}{
		{"/auth/refresh", "iam"},
		{"/api/v1/auth/login", "iam"},
		{"/api/v1/auth/me", "iam"},
		{"/api/v1/auth/change-password", "iam"},
		{"/api/v1/admin/users", "iam"},
		{"/api/v1/admin/users/u-1/roles", "iam"},
		{"/api/v1/products", "catalog"},
		{"/api/v1/categories", "catalog"},
		{"/api/v1/categories/backpacks", "catalog"},
		{"/api/v1/quotes", "catalog"},
		{"/api/v1/quotes/q-1", "catalog"},
		{"/api/v1/inventory/SKU-1", "inventory"},
		{"/api/v1/inventory/reservations", "inventory"},
		{"/api/v1/orders", "order"},
		{"/api/v1/payments/o-1/refund-reservations", "payment"},
		{"/api/v1/shipments/s-1/handover", "fulfillment"},
		{"/api/v1/shipments/s-1/stop-for-cancel", "fulfillment"},
		{"/api/v1/reconciliations/evaluate", "ledger"},
		{"/api/v1/backoffice/exception-cases", "backoffice"},
		{"/api/v1/notifications/user-1", "notification"},
		{"/api/v1/notifications/dispatch", "notification"},
		{"/api/v1/unknown", ""},
		{"/health-not-proxied", ""},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			if got := router.resolveTarget(tc.path); got != tc.want {
				t.Fatalf("resolveTarget(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestResolveTargetNoLongerServesRetiredPrefixes(t *testing.T) {
	router := NewRouter(ServiceTargets{Fulfillment: "fulfillment", Ledger: "ledger"})
	for _, path := range []string{"/api/v1/fulfillment/anything", "/api/v1/ledger/anything"} {
		if got := router.resolveTarget(path); got != "" {
			t.Fatalf("resolveTarget(%q) = %q, want no target", path, got)
		}
	}
}
