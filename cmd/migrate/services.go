package main

import (
	"fmt"
	"io/fs"

	support "github.com/hieuvu0611/commercelab-retail-be/services/backoffice-support-service/migrations"
	pricing "github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/migrations"
	fulfillment "github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/migrations"
	iam "github.com/hieuvu0611/commercelab-retail-be/services/iam-auth-service/migrations"
	inventory "github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/migrations"
	ledger "github.com/hieuvu0611/commercelab-retail-be/services/ledger-reconciliation-service/migrations"
	notification "github.com/hieuvu0611/commercelab-retail-be/services/notification-service/migrations"
	order "github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/migrations"
	payment "github.com/hieuvu0611/commercelab-retail-be/services/payment-service/migrations"
)

const dsnTemplate = "postgres://%s:%s@localhost:5432/commerce_lab?sslmode=disable&search_path=%s"

// target describes one service's schema, connection setting and embedded migrations.
type target struct {
	name       string
	dsnEnv     string
	defaultDSN string
	fs         fs.FS
}

func newTarget(name, dsnEnv, role, password, schema string, migrations fs.FS) target {
	return target{
		name:       name,
		dsnEnv:     dsnEnv,
		defaultDSN: fmt.Sprintf(dsnTemplate, role, password, schema),
		fs:         migrations,
	}
}

// allTargets lists every service schema in dependency-free order (schemas never reference each other).
func allTargets() []target {
	return []target{
		newTarget("iam", "DATABASE_URL_IAM", "iam_svc_user", "iam_secret", "iam", iam.FS),
		newTarget("pricing", "DATABASE_URL_PRICING", "pricing_svc_user", "pricing_secret", "pricing", pricing.FS),
		newTarget("inventory", "DATABASE_URL_INVENTORY", "inventory_svc_user", "inventory_secret", "inventory", inventory.FS),
		newTarget("orders", "DATABASE_URL_ORDERS", "order_svc_user", "order_secret", "orders", order.FS),
		newTarget("payments", "DATABASE_URL_PAYMENTS", "payment_svc_user", "payment_secret", "payments", payment.FS),
		newTarget("fulfillment", "DATABASE_URL_FULFILLMENT", "fulfillment_svc_user", "fulfillment_secret", "fulfillment", fulfillment.FS),
		newTarget("ledger", "DATABASE_URL_LEDGER", "ledger_svc_user", "ledger_secret", "ledger", ledger.FS),
		newTarget("support", "DATABASE_URL_SUPPORT", "support_svc_user", "support_secret", "support", support.FS),
		newTarget("notifications", "DATABASE_URL_NOTIFICATIONS", "notify_svc_user", "notify_secret", "notifications", notification.FS),
	}
}

// selectTargets returns every target when names is empty, otherwise only the named ones.
func selectTargets(names []string) ([]target, error) {
	all := allTargets()
	if len(names) == 0 {
		return all, nil
	}
	byName := make(map[string]target, len(all))
	for _, t := range all {
		byName[t.name] = t
	}
	selected := make([]target, 0, len(names))
	for _, name := range names {
		t, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("unknown service %q", name)
		}
		selected = append(selected, t)
	}
	return selected, nil
}
