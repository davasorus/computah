package agent

import (
	"net/http"
	"testing"
)

func TestSetAuth(t *testing.T) {
	// Save and restore the package global so tests don't leak state.
	saved := apiKey
	defer func() { apiKey = saved }()

	// No key configured (local server): no Authorization header.
	apiKey = ""
	req, _ := http.NewRequest("GET", "http://localhost:1234/v1/models", nil)
	setAuth(req)
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("local server should get no auth header, got %q", got)
	}

	// Key configured (cloud/proxied): Bearer header present.
	apiKey = "sk-test-123"
	req2, _ := http.NewRequest("POST", "https://api.example.com/v1/chat/completions", nil)
	setAuth(req2)
	if got := req2.Header.Get("Authorization"); got != "Bearer sk-test-123" {
		t.Errorf("authenticated endpoint should get Bearer header, got %q", got)
	}
}
