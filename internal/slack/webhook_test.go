package slack

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSend(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("missing content-type header")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewWebhookClient(srv.URL)
	err := client.SendText(context.Background(), "test message")
	if err != nil {
		t.Fatal(err)
	}
}

func TestSendError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := NewWebhookClient(srv.URL)
	err := client.SendText(context.Background(), "test")
	if err == nil {
		t.Error("expected error for 500 response")
	}
}
