package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/notification-service/internal/stopalert"
)

// maxStreamLifetime caps one connection so a screen re-authenticates regularly.
const maxStreamLifetime = 15 * time.Minute

// defaultKeepAlive is how often an idle stream sends a comment so proxies keep it open.
const defaultKeepAlive = 25 * time.Second

var warehouseIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// registerStopAlertRoutes serves the warehouse stop-alert stream. Only warehouse staff or managers whose token
// covers the warehouse may subscribe.
func registerStopAlertRoutes(mux *http.ServeMux, hub *stopalert.Hub, keepAlive time.Duration) {
	handler := auth.RequireAuth(auth.RequireRole("WAREHOUSE_STAFF", "WAREHOUSE_MGR")(
		auth.RequireWarehouseScope(func(r *http.Request) string { return r.PathValue("warehouseId") })(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { streamWarehouse(w, r, hub, keepAlive) }))))
	mux.Handle("GET /api/v1/notifications/warehouse/{warehouseId}/stream", handler)
}

func streamWarehouse(w http.ResponseWriter, r *http.Request, hub *stopalert.Hub, keepAlive time.Duration) {
	warehouse := r.PathValue("warehouseId")
	if !warehouseIDPattern.MatchString(warehouse) {
		response.Error(w, r, http.StatusBadRequest, "INVALID_WAREHOUSE_ID", "The warehouse id is invalid", nil)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		response.Error(w, r, http.StatusInternalServerError, "STREAMING_UNSUPPORTED", "Streaming is not supported", nil)
		return
	}
	claims, _ := auth.ClaimsFrom(r.Context())
	lifetime := untilExpiry(claims, time.Now())
	if lifetime <= 0 {
		response.Error(w, r, http.StatusUnauthorized, "INVALID_ACCESS_TOKEN", "The access token is invalid or expired", nil)
		return
	}

	sub := hub.Subscribe(warehouse)
	defer sub.Close()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	connected, _ := json.Marshal(map[string]string{"warehouseId": warehouse, "timestamp": time.Now().UTC().Format(time.RFC3339)})
	fmt.Fprintf(w, "event: connected\ndata: %s\n\n", connected)
	for _, m := range sub.Initial {
		writeMessage(w, m)
	}
	flusher.Flush()

	expiry := time.NewTimer(lifetime)
	defer expiry.Stop()
	tick := time.NewTicker(keepAlive)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-expiry.C:
			return
		case m, open := <-sub.C:
			if !open { // dropped for being too slow: the screen reconnects and gets the open alerts again
				return
			}
			writeMessage(w, m)
		case <-tick.C:
			fmt.Fprint(w, ": keep-alive\n\n")
		}
		flusher.Flush()
	}
}

func writeMessage(w http.ResponseWriter, m stopalert.Message) {
	data, err := json.Marshal(m.Data)
	if err != nil {
		return
	}
	if m.ID != "" {
		fmt.Fprintf(w, "id: %s\n", m.ID)
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", m.Event, data)
}

// untilExpiry is how long a stream opened now may live: until the token expires, at most maxStreamLifetime.
// A token without an expiry gets the maximum; an expired one gets zero.
func untilExpiry(claims *auth.Claims, now time.Time) time.Duration {
	if claims == nil || claims.ExpiresAt == nil {
		return maxStreamLifetime
	}
	left := claims.ExpiresAt.Time.Sub(now)
	return min(max(left, 0), maxStreamLifetime)
}
