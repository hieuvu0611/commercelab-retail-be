package main

import (
	"bufio"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/services/api-gateway/internal/proxy"
)

const warehouseStreamPath = "/api/v1/notifications/warehouse/WH-HCM-01/stream"

func TestWarehouseStreamRequiresAToken(t *testing.T) {
	u := newGateway(t)
	if rec := u.do(http.MethodGet, warehouseStreamPath, "", nil, ""); rec.Code != http.StatusUnauthorized || u.calls != 0 {
		t.Fatalf("anonymous stream = %d, upstream calls = %d", rec.Code, u.calls)
	}
}

func TestWarehouseStreamEventsReachTheClientWhileTheUpstreamStaysOpen(t *testing.T) {
	authtest.Configure(t)
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "event: connected\ndata: {}\n\n")
		w.(http.Flusher).Flush()
		<-release // the stream stays open: the first event must not wait for it to end
	}))
	defer upstream.Close()
	defer close(release)

	targets := proxy.DefaultTargetsFromEnv()
	targets.Notification = upstream.URL
	gateway := httptest.NewServer(NewServer(proxy.NewRouter(targets), false))
	defer gateway.Close()

	tok, err := auth.SignAccessToken(auth.Identity{UserID: "staff-hcm", Roles: []string{"WAREHOUSE_STAFF"}, WarehouseScopes: []string{"WH-HCM-01"}})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, gateway.URL+warehouseStreamPath, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	line := make(chan string, 1)
	go func() {
		s, _ := bufio.NewReader(res.Body).ReadString('\n')
		line <- s
	}()
	select {
	case s := <-line:
		if !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") || s != "event: connected\n" {
			t.Fatalf("content type %q first line %q", res.Header.Get("Content-Type"), s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the gateway buffered the stream")
	}
}
