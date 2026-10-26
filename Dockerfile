# Multi-stage Dockerfile cho 9 Go Microservices (tối ưu cho giới hạn <= 250MB RAM & <= 0.25 CPU)
FROM golang:1.26-alpine AS builder

ARG SERVICE_NAME
WORKDIR /src

COPY go.mod ./
COPY pkg ./pkg
COPY contracts ./contracts
COPY services/${SERVICE_NAME} ./services/${SERVICE_NAME}

# Strip DWARF/symbol table (-s -w) để giảm kích thước binary (~6MB) và giảm RSS RAM khởi điểm (~8-12MB)
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/service ./services/${SERVICE_NAME}/cmd/main.go

FROM alpine:3.20
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app

COPY --from=builder /out/service /app/service

# Khóa cứng runtime Go cho cấu hình <= 0.25 CPU và <= 250MB RAM
ENV GOMAXPROCS=1
ENV GOMEMLIMIT=220MiB

ENTRYPOINT ["/app/service"]
