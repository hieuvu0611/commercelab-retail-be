package response

import (
	"encoding/json"
	"net/http"
)

// Metadata khớp 100% với fe/src/types/api-response.ts
type Metadata struct {
	Total     int64 `json:"total"`
	Page      int   `json:"page"`
	PageCount int   `json:"pageCount"`
	Limit     int   `json:"limit"`
}

// ApiResponse<T> khớp 100% với fe/src/types/api-response.ts
type ApiResponse[T any] struct {
	Message  string    `json:"message,omitempty"`
	Data     T         `json:"data"`
	Metadata *Metadata `json:"metadata,omitempty"`
}

// ErrorResponse chuẩn RFC 9457 mở rộng và tương thích axios-config.ts của fe/
type ErrorResponse struct {
	Message       string              `json:"message"`
	Code          string              `json:"code"`
	Status        int                 `json:"status"`
	CorrelationID string              `json:"correlationId,omitempty"`
	Errors        map[string][]string `json:"errors,omitempty"`
}

func JSON[T any](w http.ResponseWriter, status int, message string, data T, meta *Metadata) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ApiResponse[T]{
		Message:  message,
		Data:     data,
		Metadata: meta,
	})
}

func Error(w http.ResponseWriter, r *http.Request, status int, code string, message string, fieldErrors map[string][]string) {
	corrID := ""
	if r != nil {
		corrID = r.Header.Get("X-Correlation-ID")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorResponse{
		Message:       message,
		Code:          code,
		Status:        status,
		CorrelationID: corrID,
		Errors:        fieldErrors,
	})
}
