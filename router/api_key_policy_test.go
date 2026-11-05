package router_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tigerowo/infinite-canvas/middleware"
	"github.com/tigerowo/infinite-canvas/service"
)

// The independent inventory makes a newly registered protected route fail this
// test until its API-key policy is explicitly reviewed.
var expectedScopeRoutes = map[string][]string{
	"read": {
		"GET /api/auth/me", "GET /api/prompts", "GET /api/assets",
		"GET /api/v1/tts/voices", "GET /api/v1/canvas/image-tasks", "POST /api/v1/canvas/image-tasks/status",
		"GET /api/v1/canvas/image-tasks/:id", "GET /api/v1/canvas/audio-tasks/:id", "GET /api/v1/video-tasks",
		"GET /api/v1/videos/:id", "GET /api/v1/videos/:id/content", "GET /api/v1/workflows", "GET /api/v1/agent-skills",
		"POST /api/v1/storage/measure", "POST /api/v1/workflow-providers/runninghub/inspect", "GET /api/v1/workflow-tasks/:id",
		"GET /api/v1/canvas/projects", "GET /api/v1/user-data/image-history", "GET /api/v1/generation-logs/videos",
		"GET /api/v1/generation-logs/images", "GET /api/v1/user-data/assets",
	},
	"generate": {
		"POST /api/v1/images/generations", "POST /api/v1/images/edits", "POST /api/v1/responses", "POST /api/v1/chat/completions",
		"POST /api/v1/audio/speech", "POST /api/v1/canvas/image-tasks", "POST /api/v1/canvas/audio-tasks", "POST /api/v1/videos",
		"POST /api/v1/workflows/agent-draft", "POST /api/v1/workflow-tasks",
	},
	"write": {
		"POST /api/v1/canvas/tasks/stop", "POST /api/v1/ai-logs",
		"POST /api/v1/files", "POST /api/v1/files/direct",
	},
	"read+write": {
		"POST /api/v1/canvas/projects",
		"POST /api/v1/user-data/image-history",
		"POST /api/v1/user-data/assets",
		"POST /api/v1/generation-logs/videos",
		"POST /api/v1/generation-logs/images",
		"POST /api/v1/workflows",
		"POST /api/v1/agent-skills",
	},
	"delete": {
		"DELETE /api/v1/canvas/image-tasks/:id", "DELETE /api/v1/video-tasks/:id", "DELETE /api/v1/workflows/:id", "DELETE /api/v1/agent-skills/:id",
		"DELETE /api/v1/files/:id", "DELETE /api/v1/files/:id/record", "POST /api/v1/canvas/projects/delete",
		"POST /api/v1/generation-logs/videos/delete", "DELETE /api/v1/generation-logs/videos/:id",
		"POST /api/v1/generation-logs/images/delete", "DELETE /api/v1/generation-logs/images/:id",
	},
}

func exerciseAPIKeyRoutePolicy(t *testing.T, actual *gin.Engine, keys map[string]string, userJWT, adminJWT, adminKey, finiteKey string) {
	t.Helper()
	expected := map[string]string{}
	for scope, routes := range expectedScopeRoutes {
		for _, route := range routes {
			if _, duplicate := expected[route]; duplicate {
				t.Fatalf("duplicate policy inventory route: %s", route)
			}
			expected[route] = scope
		}
	}
	seen := map[string]bool{}
	routesChecked := 0
	for _, route := range actual.Routes() {
		optional := route.Path == "/api/auth/me" || route.Path == "/api/prompts" || route.Path == "/api/assets"
		admin := strings.HasPrefix(route.Path, "/api/admin/") && route.Path != "/api/admin/login"
		if !optional && !admin && !strings.HasPrefix(route.Path, "/api/v1/") {
			continue
		}
		id := route.Method + " " + route.Path
		requiredScope := expected[id]
		sessionOnly := admin || strings.HasPrefix(route.Path, "/api/v1/api-keys") || strings.HasPrefix(route.Path, "/api/v1/user-config") || strings.HasPrefix(route.Path, "/api/v1/comfy-bridges") || route.Path == "/api/v1/canvas/projects/sync"
		if !sessionOnly && requiredScope == "" {
			t.Fatalf("unreviewed protected route: %s", id)
		}
		seen[id] = true
		routesChecked++
		gate := gin.New()
		auth := middleware.UserAuth
		if admin {
			auth = middleware.AdminAuth
		} else if optional {
			auth = middleware.OptionalAuth
		}
		gate.Handle(route.Method, route.Path, auth, func(c *gin.Context) {
			_, hasKey := service.APIKeyIDFromContext(c.Request.Context())
			c.JSON(http.StatusOK, gin.H{"hasKey": hasKey})
		})
		path := strings.ReplaceAll(route.Path, ":id", "resource-id")
		call := func(authorization string) *httptest.ResponseRecorder {
			r := httptest.NewRequest(route.Method, path, nil)
			if authorization != "" {
				r.Header.Set("Authorization", authorization)
			}
			w := httptest.NewRecorder()
			gate.ServeHTTP(w, r)
			return w
		}
		for scope, key := range keys {
			want := http.StatusForbidden
			if !sessionOnly {
				allowed := true
				for _, required := range strings.Split(requiredScope, "+") {
					if scope != "all" && !slices.Contains(strings.Split(scope, "+"), required) {
						allowed = false
					}
				}
				if allowed {
					want = http.StatusOK
				}
			}
			w := call("bEaReR " + key)
			if w.Code != want {
				t.Fatalf("%s key scope=%s: HTTP %d, want %d; %s", id, scope, w.Code, want, w.Body)
			}
			if want == http.StatusOK && !strings.Contains(w.Body.String(), `"hasKey":true`) {
				t.Fatalf("%s lost key identity in request context", id)
			}
		}
		if sessionOnly && call("Bearer "+adminKey).Code != http.StatusForbidden {
			t.Fatalf("administrator key bypassed session-only route: %s", id)
		}
		wantUser := http.StatusOK
		if admin {
			wantUser = http.StatusForbidden
		}
		if w := call("Bearer " + userJWT); w.Code != wantUser {
			t.Fatalf("%s user session: HTTP %d, want %d", id, w.Code, wantUser)
		}
		if w := call("Bearer " + adminJWT); w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"hasKey":true`) {
			t.Fatalf("%s administrator session failed or retained key context: HTTP %d", id, w.Code)
		}
		wantAnonymous := http.StatusUnauthorized
		if optional {
			wantAnonymous = http.StatusOK
		}
		if w := call(""); w.Code != wantAnonymous {
			t.Fatalf("%s anonymous: HTTP %d, want %d", id, w.Code, wantAnonymous)
		}
		if optional {
			for _, header := range []string{"Bearer ic_invalid", "Bearer bad-jwt", "Basic bad", "Bearer one two"} {
				if w := call(header); w.Code != http.StatusUnauthorized {
					t.Fatalf("%s downgraded bad credentials to anonymous: %s HTTP %d", id, header, w.Code)
				}
			}
		}
	}
	for route := range expected {
		if !seen[route] {
			t.Fatalf("policy inventory names a missing route: %s", route)
		}
	}
	t.Logf("checked %d protected/optional routes against four individual scopes, read+write, all scopes, sessions and anonymous access", routesChecked)

	unknown := gin.New()
	unknown.POST("/api/v1/future-resource", middleware.UserAuth, func(c *gin.Context) { c.Status(http.StatusNoContent) })
	unknown.PATCH("/api/v1/canvas/projects", middleware.UserAuth, func(c *gin.Context) { c.Status(http.StatusNoContent) })
	r := httptest.NewRequest(http.MethodPost, "/api/v1/future-resource", nil)
	r.Header.Set("Authorization", "Bearer "+keys["all"])
	w := httptest.NewRecorder()
	unknown.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("an unlisted future route allowed an API key: HTTP %d", w.Code)
	}

	for _, path := range []string{"/api/v1/future-resource", "/api/v1/canvas/projects"} {
		method := http.MethodPost
		if path == "/api/v1/canvas/projects" {
			method = http.MethodPatch
		}
		for _, token := range []string{keys["all"], userJWT} {
			r := httptest.NewRequest(method, path, nil)
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			unknown.ServeHTTP(w, r)
			want := http.StatusForbidden
			if token == userJWT {
				want = http.StatusNoContent
			}
			if w.Code != want {
				t.Fatalf("unknown method/path policy %s %s: HTTP %d, want %d", method, path, w.Code, want)
			}
		}
	}

	// Empty sync must not let a write-only key retrieve all saved canvases.
	r = httptest.NewRequest(http.MethodPost, "/api/v1/canvas/projects/sync", strings.NewReader(`{"projects":[]}`))
	r.Header.Set("Authorization", "Bearer "+keys["write"])
	w = httptest.NewRecorder()
	actual.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "owner-project") {
		t.Fatalf("write key read all canvases through sync: HTTP %d %s", w.Code, w.Body)
	}

	// Exercise the real management handler boundary: a finite key cannot mint a
	// permanent credential, even with every business scope.
	r = httptest.NewRequest(http.MethodPost, "/api/v1/api-keys", strings.NewReader(`{"name":"forbidden-child","expiresAt":null}`))
	r.Header.Set("Authorization", "Bearer "+finiteKey)
	w = httptest.NewRecorder()
	actual.ServeHTTP(w, r)
	var response apiKeyResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != http.StatusForbidden || response.Code == 0 {
		t.Fatalf("key could derive another key: HTTP %d, %s", w.Code, w.Body)
	}
}
