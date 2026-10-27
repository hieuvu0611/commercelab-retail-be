package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
)

// Chain áp dụng danh sách HTTP middlewares (CORS, Correlation-ID, Recovery)
func Chain(h http.Handler) http.Handler {
	return CORS(CorrelationID(h))
}

func CorrelationID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		corrID := r.Header.Get("X-Correlation-ID")
		if corrID == "" {
			var b [8]byte
			_, _ = rand.Read(b[:])
			corrID = "req-" + hex.EncodeToString(b[:])
			r.Header.Set("X-Correlation-ID", corrID)
		}
		w.Header().Set("X-Correlation-ID", corrID)
		next.ServeHTTP(w, r)
	})
}

func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Idempotency-Key, X-Correlation-ID, X-User-ID, X-User-Role, X-Warehouse-Scope")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireWarehouseScope bảo vệ bất biến I08 (SC12): Nhân viên Kho A không được sửa Kho B
func RequireWarehouseScope(targetWarehouse string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userScope := r.Header.Get("X-Warehouse-Scope")
		if userScope != "" && userScope != "ALL" && userScope != targetWarehouse {
			response.Error(w, r, http.StatusForbidden, "FORBIDDEN_WAREHOUSE_SCOPE",
				"Quyền hạn không hợp lệ: Không được phép thao tác ngoài phạm vi kho "+userScope, nil)
			return
		}
		next(w, r)
	}
}
