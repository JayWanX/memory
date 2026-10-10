package embed

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewClient_DefaultModel(t *testing.T) {
	t.Setenv("MEMORY_EMBED_MODEL", "")
	c := NewClient()
	if c.Model() != DefaultModel {
		t.Errorf("default model: want %q, got %q", DefaultModel, c.Model())
	}
}

func TestNewClient_EnvOverride(t *testing.T) {
	t.Setenv("MEMORY_EMBED_MODEL", "env-model:latest")
	c := NewClient()
	if c.Model() != "env-model:latest" {
		t.Errorf("env-derived model: want %q, got %q", "env-model:latest", c.Model())
	}
}

func TestNewClientWithModel_Explicit(t *testing.T) {
	t.Setenv("MEMORY_EMBED_MODEL", "")
	c := NewClientWithModel("explicit-model:v1")
	if c.Model() != "explicit-model:v1" {
		t.Errorf("explicit model: want %q, got %q", "explicit-model:v1", c.Model())
	}
}

func TestNewClientWithModel_ExplicitWinsOverEnv(t *testing.T) {
	t.Setenv("MEMORY_EMBED_MODEL", "env-model")
	c := NewClientWithModel("explicit-model")
	if c.Model() != "explicit-model" {
		t.Errorf("explicit-over-env: want %q, got %q", "explicit-model", c.Model())
	}
}

func TestNewClientWithModel_EmptyFallsBackToEnv(t *testing.T) {
	t.Setenv("MEMORY_EMBED_MODEL", "env-model")
	c := NewClientWithModel("")
	if c.Model() != "env-model" {
		t.Errorf("empty-falls-to-env: want %q, got %q", "env-model", c.Model())
	}
}

func TestNewClientWithModel_EmptyAndNoEnvFallsBackToDefault(t *testing.T) {
	t.Setenv("MEMORY_EMBED_MODEL", "")
	c := NewClientWithModel("")
	if c.Model() != DefaultModel {
		t.Errorf("empty-and-no-env: want %q, got %q", DefaultModel, c.Model())
	}
}

func TestNewClientWithModel_DefaultEndpoint(t *testing.T) {
	t.Setenv("MEMORY_EMBED_ENDPOINT", "")
	c := NewClientWithModel("any-model")
	if c.Endpoint() != DefaultEndpoint {
		t.Errorf("default endpoint: want %q, got %q", DefaultEndpoint, c.Endpoint())
	}
}

// TestNewClientWithModel_ExplicitEmptyTreatedAsUnset documents the deliberate
// choice that --embed-model "" falls through to env-var/default rather than
// being rejected. This matches how the existing --data-dir flag handles empty
// strings and avoids surprises when a user clears the flag via shell scripting
// such as `memory --embed-model "$MAYBE_MODEL" ...`.
func TestNewClientWithModel_ExplicitEmptyTreatedAsUnset(t *testing.T) {
	t.Setenv("MEMORY_EMBED_MODEL", "env-model")
	c := NewClientWithModel("")
	if c.Model() != "env-model" {
		t.Errorf("explicit empty should fall through to env: want %q, got %q", "env-model", c.Model())
	}

	t.Setenv("MEMORY_EMBED_MODEL", "")
	c = NewClientWithModel("")
	if c.Model() != DefaultModel {
		t.Errorf("explicit empty + no env should fall through to default: want %q, got %q", DefaultModel, c.Model())
	}
}

func TestOllamaEndpointWithTrailingSlash(t *testing.T) {
	t.Setenv("MEMORY_EMBED_PROTOCOL", "ollama")
	t.Setenv("MEMORY_EMBED_API_KEY", "must-not-be-sent")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("expected Ollama request without Authorization header, got %q", got)
		}
		switch r.URL.Path {
		case "/api/tags":
			w.WriteHeader(http.StatusOK)
		case "/api/embed":
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			if got := r.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("expected application/json, got %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"embeddings":[[0.1,0.2,0.3]]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	t.Setenv("MEMORY_EMBED_ENDPOINT", srv.URL+"/")
	c := NewClient()
	if !c.Available() {
		t.Fatal("expected Available() true for trailing-slash Ollama endpoint")
	}
	vec, err := c.Embed("hello")
	if err != nil {
		t.Fatalf("Embed with trailing-slash Ollama endpoint: %v", err)
	}
	if len(vec) != 3 {
		t.Fatalf("expected 3 dims, got %d", len(vec))
	}
}

// keepAliveSent embeds once against a stub server under the given protocol
// and returns the keep_alive field of the request body ("" when absent).
func keepAliveSent(t *testing.T, protocol, route string) string {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if protocol == "openai" {
			_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1]}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"embeddings":[[0.1]]}`))
	}))
	defer srv.Close()
	t.Setenv("MEMORY_EMBED_PROTOCOL", protocol)
	t.Setenv("MEMORY_EMBED_ENDPOINT", srv.URL+route)
	if _, err := NewClient().Embed("hello"); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	s, _ := got["keep_alive"].(string)
	return s
}

func TestOllamaEmbed_SendsDefaultKeepAlive(t *testing.T) {
	t.Setenv("MEMORY_EMBED_KEEP_ALIVE", "")
	if got := keepAliveSent(t, "ollama", ""); got != DefaultKeepAlive {
		t.Fatalf("keep_alive = %q, want %q", got, DefaultKeepAlive)
	}
}

func TestOllamaEmbed_KeepAliveEnvOverride(t *testing.T) {
	t.Setenv("MEMORY_EMBED_KEEP_ALIVE", "2h")
	if got := keepAliveSent(t, "ollama", ""); got != "2h" {
		t.Fatalf("keep_alive = %q, want %q", got, "2h")
	}
}

func TestOpenAIEmbed_OmitsKeepAlive(t *testing.T) {
	t.Setenv("MEMORY_EMBED_KEEP_ALIVE", "")
	if got := keepAliveSent(t, "openai", "/v1"); got != "" {
		t.Fatalf("keep_alive = %q, want it omitted", got)
	}
}
