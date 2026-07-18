package connector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchModelsUsesPlatformProtocol(t *testing.T) {
	tests := []struct {
		name       string
		platform   string
		wantPath   string
		wantHeader string
		response   any
	}{
		{name: "openai", platform: "openai", wantPath: "/v1/models", wantHeader: "Bearer test-key", response: map[string]any{"data": []map[string]any{{"id": "gpt-4.1-mini"}, {"id": "gpt-4.1"}}}},
		{name: "anthropic", platform: "anthropic", wantPath: "/v1/models", wantHeader: "test-key", response: map[string]any{"data": []map[string]any{{"id": "claude-sonnet-4-5"}, {"id": "claude-haiku-4-5"}}}},
		{name: "gemini", platform: "gemini", wantPath: "/v1beta/models", wantHeader: "test-key", response: map[string]any{"models": []map[string]any{{"name": "models/gemini-2.5-flash"}, {"name": "models/gemini-2.5-pro"}}}},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.URL.Path != item.wantPath {
					t.Fatalf("path = %q, want %q", request.URL.Path, item.wantPath)
				}
				header := request.Header.Get("Authorization")
				if item.platform == "anthropic" {
					header = request.Header.Get("x-api-key")
				}
				if item.platform == "gemini" {
					header = request.Header.Get("x-goog-api-key")
				}
				if header != item.wantHeader {
					t.Fatalf("auth header = %q, want %q", header, item.wantHeader)
				}
				_ = json.NewEncoder(w).Encode(item.response)
			}))
			defer server.Close()

			models, err := FetchModels(context.Background(), server.Client(), server.URL, item.platform, "test-key")
			if err != nil {
				t.Fatalf("fetch models: %v", err)
			}
			if len(models) != 2 || models[0] == "" || models[1] == "" {
				t.Fatalf("models = %#v", models)
			}
		})
	}
}

func TestFetchModelsLoadsEveryAnthropicPage(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests++
		if request.URL.Query().Get("limit") != "1000" {
			t.Fatalf("limit = %q", request.URL.Query().Get("limit"))
		}
		if requests == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"id": "claude-haiku-4-5"}}, "has_more": true, "last_id": "claude-haiku-4-5",
			})
			return
		}
		if request.URL.Query().Get("after_id") != "claude-haiku-4-5" {
			t.Fatalf("after_id = %q", request.URL.Query().Get("after_id"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"id": "claude-sonnet-4-5"}}, "has_more": false,
		})
	}))
	defer server.Close()

	models, err := FetchModels(context.Background(), server.Client(), server.URL, "anthropic", "test-key")
	if err != nil {
		t.Fatalf("fetch paginated models: %v", err)
	}
	if requests != 2 || len(models) != 2 || models[0] != "claude-haiku-4-5" || models[1] != "claude-sonnet-4-5" {
		t.Fatalf("requests = %d, models = %#v", requests, models)
	}
}
