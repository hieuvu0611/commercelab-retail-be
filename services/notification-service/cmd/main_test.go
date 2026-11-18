package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/internalauth"
	"github.com/hieuvu0611/commercelab-retail-be/services/notification-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/notification-service/internal/stopalert"
)

const testInternalToken = "test-internal-service-token-32-bytes"

func TestNotificationInboxAndStreamRequireRecipientOwnership(t *testing.T) {
	authtest.Configure(t)
	internalauth.Configure(testInternalToken)

	dispatcher := domain.NewDispatcher()
	dispatcher.Dispatch(domain.NotificationItem{
		NotificationID: "NOTI-1",
		EventID:        "evt-1",
		RecipientID:    "user-lan",
		Channel:        "IN_APP",
		Category:       "ORDER_CONFIRMED",
		Title:          "Order confirmed",
		Message:        "Your order is confirmed",
	})

	srv := httptest.NewServer(NewServer(dispatcher, stopalert.NewHub(), 20*time.Millisecond))
	t.Cleanup(srv.Close)

	lanTok, err := auth.SignAccessToken(auth.Identity{UserID: "user-lan", Roles: []string{"CUSTOMER"}})
	if err != nil {
		t.Fatal(err)
	}
	otherTok, err := auth.SignAccessToken(auth.Identity{UserID: "user-other", Roles: []string{"CUSTOMER"}})
	if err != nil {
		t.Fatal(err)
	}

	do := func(method, path, bearer, internal string, body []byte) *http.Response {
		req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if internal != "" {
			req.Header.Set(internalauth.Header, internal)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = res.Body.Close() })
		return res
	}

	// Unauthenticated inbox, mark-read, read-all, and stream fail with 401
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/notifications/user-lan"},
		{http.MethodPatch, "/api/v1/notifications/user-lan/NOTI-1/read"},
		{http.MethodPatch, "/api/v1/notifications/user-lan/read-all"},
		{http.MethodGet, "/api/v1/notifications/user-lan/stream"},
	} {
		if res := do(tc.method, tc.path, "", "", nil); res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("anonymous %s %s = %d, want 401", tc.method, tc.path, res.StatusCode)
		}
	}

	// Another user's token fails with 403
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/notifications/user-lan"},
		{http.MethodPatch, "/api/v1/notifications/user-lan/NOTI-1/read"},
		{http.MethodPatch, "/api/v1/notifications/user-lan/read-all"},
		{http.MethodGet, "/api/v1/notifications/user-lan/stream"},
	} {
		if res := do(tc.method, tc.path, otherTok, "", nil); res.StatusCode != http.StatusForbidden {
			t.Fatalf("other user %s %s = %d, want 403", tc.method, tc.path, res.StatusCode)
		}
	}

	// Dispatch requires internal token (bearer fails 401)
	payload, _ := json.Marshal(domain.NotificationItem{
		EventID:     "evt-dispatch-1",
		RecipientID: "user-lan",
		Category:    "PROMO",
		Title:       "Promo",
		Message:     "Hello",
	})
	if res := do(http.MethodPost, "/api/v1/notifications/dispatch", lanTok, "", payload); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("dispatch with user bearer = %d, want 401", res.StatusCode)
	}
	if res := do(http.MethodPost, "/api/v1/notifications/dispatch", "", testInternalToken, payload); res.StatusCode != http.StatusOK {
		t.Fatalf("dispatch with internal token = %d, want 200", res.StatusCode)
	}

	// Owner reads inbox & marks read
	if res := do(http.MethodGet, "/api/v1/notifications/user-lan", lanTok, "", nil); res.StatusCode != http.StatusOK {
		t.Fatalf("owner list = %d, want 200", res.StatusCode)
	}
	if res := do(http.MethodPatch, "/api/v1/notifications/user-lan/NOTI-1/read", lanTok, "", nil); res.StatusCode != http.StatusOK {
		t.Fatalf("owner mark read = %d, want 200", res.StatusCode)
	}
	if res := do(http.MethodPatch, "/api/v1/notifications/user-lan/read-all", lanTok, "", nil); res.StatusCode != http.StatusOK {
		t.Fatalf("owner read-all = %d, want 200", res.StatusCode)
	}

	// Owner receives SSE connected event
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/notifications/user-lan/stream", nil)
	req.Header.Set("Authorization", "Bearer "+lanTok)
	sseRes, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sseRes.Body.Close() }()
	if sseRes.StatusCode != http.StatusOK || !strings.HasPrefix(sseRes.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("sse stream status = %d", sseRes.StatusCode)
	}
	ev := readEvent(t, bufio.NewReader(sseRes.Body))
	if ev.event != "connected" {
		t.Fatalf("expected connected sse event, got %+v", ev)
	}
}
