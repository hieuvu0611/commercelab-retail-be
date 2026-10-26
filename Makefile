.PHONY: infra-up infra-down benchmark-up benchmark-down benchmark-stats build test run-gateway run-iam run-catalog run-inventory run-order run-payment run-fulfillment run-ledger run-notification run-backoffice

infra-up:
	docker compose up -d

infra-down:
	docker compose down

# Chạy toàn bộ 14 containers (4 Infra + 10 Microservices) với trần cứng <= 250MB RAM & <= 0.25 CPU/service
benchmark-up:
	docker compose --profile app up -d --build

benchmark-down:
	docker compose --profile app down

# Đo lường trực tiếp CPU / RAM thực tế so với trần 250MB / 0.25 CPU
benchmark-stats:
	docker stats --format "table {{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}\t{{.MemPerc}}\t{{.NetIO}}"

build:
	go build ./...

test:
	go test -v -race ./...

run-gateway:
	go run ./services/api-gateway/cmd/main.go

run-iam:
	go run ./services/iam-auth-service/cmd/main.go

run-catalog:
	go run ./services/catalog-pricing-service/cmd/main.go

run-inventory:
	go run ./services/inventory-warehouse-service/cmd/main.go

run-order:
	go run ./services/order-checkout-service/cmd/main.go

run-payment:
	go run ./services/payment-service/cmd/main.go

run-fulfillment:
	go run ./services/fulfillment-shipping-service/cmd/main.go

run-ledger:
	go run ./services/ledger-reconciliation-service/cmd/main.go

run-notification:
	go run ./services/notification-service/cmd/main.go

run-backoffice:
	cd services/backoffice-support-service && pnpm start:dev
