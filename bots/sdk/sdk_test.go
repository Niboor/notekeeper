package sdk

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientAuthenticatesAndForwardsRequestID(t *testing.T) {
	var gotAuth, gotID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotID = r.Header.Get("Authorization"), r.Header.Get("X-Request-ID")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"test"}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL, "nkb.abc.secret", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.GetBotVersionWithResponse(WithRequestID(context.Background(), "req-1"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.JSON200 == nil || resp.JSON200.Version != "test" {
		t.Fatalf("unexpected response: %+v", resp.JSON200)
	}
	if gotAuth != "Bearer nkb.abc.secret" || gotID != "req-1" {
		t.Fatalf("headers: auth=%q id=%q", gotAuth, gotID)
	}
}
