package digest

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSlackClientPostMessage(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	c := NewSlackClient("xoxb-test")
	c.endpoint = srv.URL
	if err := c.PostMessage(context.Background(), "C123", "hello *world*"); err != nil {
		t.Fatalf("PostMessage: %v", err)
	}
	if gotAuth != "Bearer xoxb-test" {
		t.Errorf("auth = %q", gotAuth)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("body not JSON: %v (%s)", err, gotBody)
	}
	if payload["channel"] != "C123" || payload["text"] != "hello *world*" {
		t.Errorf("payload = %v", payload)
	}
}

func TestSlackClientAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"ok":false,"error":"channel_not_found"}`)
	}))
	defer srv.Close()

	c := NewSlackClient("xoxb-test")
	c.endpoint = srv.URL
	err := c.PostMessage(context.Background(), "C123", "hi")
	if err == nil || !strings.Contains(err.Error(), "channel_not_found") {
		t.Errorf("expected channel_not_found error, got %v", err)
	}
}
