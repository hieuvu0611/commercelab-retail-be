package response

import (
	"encoding/json"
	"log"
	"net/http"
)

// Metadata describes a paginated collection.
type Metadata struct {
	Total     int64 `json:"total"`
	Page      int   `json:"page"`
	PageCount int   `json:"pageCount"`
	Limit     int   `json:"limit"`
}

// ApiResponse is the common JSON success envelope.
type ApiResponse[T any] struct {
	Message  string    `json:"message,omitempty"`
	Data     T         `json:"data"`
	Metadata *Metadata `json:"metadata,omitempty"`
}

// ErrorResponse is the project's JSON error envelope.
type ErrorResponse struct {
	Message       string              `json:"message"`
	Code          string              `json:"code"`
	Status        int                 `json:"status"`
	CorrelationID string              `json:"correlationId,omitempty"`
	Errors        map[string][]string `json:"errors,omitempty"`
}

func JSON[T any](w http.ResponseWriter, status int, message string, data T, meta *Metadata) {
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	payload, err := json.Marshal(ApiResponse[T]{Message: message, Data: data, Metadata: meta})
	if err != nil {
		log.Printf("cannot encode response: %v", err)
		Error(w, nil, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", nil)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if _, err := w.Write(append(payload, '\n')); err != nil {
		log.Printf("cannot write response: %v", err)
	}
}

func Error(w http.ResponseWriter, r *http.Request, status int, code string, message string, fieldErrors map[string][]string) {
	corrID := w.Header().Get("X-Correlation-ID")
	if r != nil {
		corrID = r.Header.Get("X-Correlation-ID")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if status >= 500 {
		fieldErrors = nil
		switch status {
		case http.StatusBadGateway:
			message = "Upstream service error"
		case http.StatusServiceUnavailable:
			message = "Service unavailable"
		case http.StatusGatewayTimeout:
			message = "Upstream service timed out"
		default:
			message = "Internal server error"
		}
	}
	if err := json.NewEncoder(w).Encode(ErrorResponse{
		Message:       message,
		Code:          code,
		Status:        status,
		CorrelationID: corrID,
		Errors:        fieldErrors,
	}); err != nil {
		log.Printf("cannot write error response: %v", err)
	}
}
