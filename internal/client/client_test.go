package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// testServer stands up a fake MetaMCP: a better-auth sign-in endpoint plus a
// tRPC surface that asserts the exact wire format. Asserting the format is the
// point — the provider talks to an undocumented API, so a regression in the
// envelope would otherwise only surface against a live instance.
type testServer struct {
	*httptest.Server

	// signInOK controls whether /api/auth/sign-in/email succeeds.
	signInOK bool
	// signInStatus lets a test force a specific failure status.
	signInStatus int

	// calls records "<METHOD> <procedure> <input>" for each tRPC request.
	calls []string
	// responders maps a procedure to the JSON `data` value to return.
	responders map[string]any
	// errors maps a procedure to a tRPC error to return.
	errors map[string]map[string]any

	sawCookie string
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	ts := &testServer{
		signInOK:   true,
		responders: map[string]any{},
		errors:     map[string]map[string]any{},
	}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/auth/sign-in/email", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !ts.signInOK {
			w.WriteHeader(ts.signInStatus)
			_, _ = w.Write([]byte(`{"code":"INVALID_EMAIL_OR_PASSWORD"}`))
			return
		}
		// A realistic better-auth session cookie.
		http.SetCookie(w, &http.Cookie{
			Name:  "better-auth.session_token",
			Value: "test-session-token",
			Path:  "/",
		})
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"user":{"email":"` + body.Email + `"}}`))
	})

	// A pattern without a trailing slash is an exact match in Go's ServeMux, so
	// this must be a prefix pattern ("/trpc/") and the procedure stripped by
	// hand — otherwise every tRPC call 404s and the tests cannot see the wire
	// format they exist to assert.
	mux.HandleFunc("/trpc/", func(w http.ResponseWriter, r *http.Request) {
		proc := strings.TrimPrefix(r.URL.Path, "/trpc/frontend.")
		if proc == r.URL.Path {
			http.NotFound(w, r)
			return
		}
		if c, err := r.Cookie("better-auth.session_token"); err == nil {
			ts.sawCookie = c.Value
		} else if h := r.Header.Get("Cookie"); h != "" {
			ts.sawCookie = h
		}

		var input string
		if r.Method == http.MethodGet {
			// Queries must carry the input as a URL-encoded ?input= parameter.
			if r.URL.Query().Get("batch") != "1" {
				http.Error(w, "missing batch=1", http.StatusBadRequest)
				return
			}
			raw, err := url.QueryUnescape(r.URL.Query().Get("input"))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			input = raw
		} else {
			// Mutations must POST the envelope as the body.
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			input = string(raw)
		}

		ts.calls = append(ts.calls, fmt.Sprintf("%s %s %s", r.Method, proc, input))

		if e, ok := ts.errors[proc]; ok {
			_ = json.NewEncoder(w).Encode([]any{map[string]any{"error": e}})
			return
		}
		data, ok := ts.responders[proc]
		if !ok {
			_ = json.NewEncoder(w).Encode([]any{map[string]any{
				"error": map[string]any{
					"message": "No procedure on path " + proc,
					"code":    -32004,
					"data":    map[string]any{"code": "NOT_FOUND", "path": proc},
				},
			}})
			return
		}
		_ = json.NewEncoder(w).Encode([]any{map[string]any{
			"result": map[string]any{"data": data},
		}})
	})

	ts.Server = httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func (s *testServer) client(t *testing.T) *Client {
	t.Helper()
	c, err := New(Config{
		Endpoint: s.URL,
		Email:    "richard@example.com",
		Password: "not-a-real-password",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestSignInAndQueryFormat(t *testing.T) {
	ts := newTestServer(t)
	ts.responders["namespaces.list"] = map[string]any{
		"success": true,
		"data": []map[string]any{{
			"uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			"name": "demo", "description": nil,
			"created_at": "2026-01-01", "updated_at": "2026-01-01", "user_id": nil,
		}},
	}

	c := ts.client(t)
	got, err := c.ListNamespaces(context.Background())
	if err != nil {
		t.Fatalf("ListNamespaces: %v", err)
	}
	if len(got) != 1 || got[0].Name != "demo" {
		t.Fatalf("unexpected result: %+v", got)
	}

	// The cookie obtained at sign-in must be presented on the tRPC call.
	if ts.sawCookie == "" {
		t.Error("no session cookie was sent with the tRPC request")
	}

	// A query must be a GET carrying {"0":{"json":null}}.
	last := ts.calls[len(ts.calls)-1]
	if !strings.HasPrefix(last, "GET namespaces.list ") {
		t.Errorf("expected a GET for a query, got %q", last)
	}
	if !strings.Contains(last, `{"0":{"json":null}}`) {
		t.Errorf("query envelope wrong: %q", last)
	}
}

func TestMutationsUsePostWithInputInBody(t *testing.T) {
	ts := newTestServer(t)
	ts.responders["namespaces.create"] = map[string]any{
		"success": true,
		"data": map[string]any{
			"uuid": "11111111-2222-3333-4444-555555555555",
			"name": "from-test", "description": "d",
			"created_at": "2026-01-01", "updated_at": "2026-01-01", "user_id": nil,
		},
	}

	c := ts.client(t)
	_, err := c.CreateNamespace(context.Background(), NamespaceInput{Name: "from-test"})
	if err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}

	last := ts.calls[len(ts.calls)-1]
	if !strings.HasPrefix(last, "POST namespaces.create ") {
		t.Fatalf("expected POST for a mutation, got %q", last)
	}
	if !strings.Contains(last, `"name":"from-test"`) {
		t.Errorf("mutation body missing input: %q", last)
	}
}

func TestMutationVerbClassification(t *testing.T) {
	// Getting this wrong yields HTTP 405 against a live instance, so pin it.
	cases := map[string]bool{
		"namespaces.create":             true,
		"namespaces.update":             true,
		"namespaces.delete":             true,
		"endpoints.upsert":              true,
		"tools.sync":                    true,
		"config.setSignupDisabled":      true,
		"namespaces.refreshTools":       true,
		"namespaces.list":               false,
		"namespaces.get":                false,
		"config.getAllConfigs":          false,
		"apiKeys.list":                  false,
		"namespaces.updateServerStatus": true,
	}
	for proc, want := range cases {
		if got := isMutation(proc); got != want {
			t.Errorf("isMutation(%q) = %v, want %v", proc, got, want)
		}
	}
}

func TestTolerantEnvelopeDecoding(t *testing.T) {
	ts := newTestServer(t)
	// A namespace with nullable description and user_id must decode.
	ts.responders["namespaces.get"] = map[string]any{
		"success": true,
		"data": map[string]any{
			"uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "name": "x",
			"description": nil, "created_at": "t", "updated_at": "t", "user_id": nil,
		},
	}
	c := ts.client(t)
	ns, err := c.GetNamespace(context.Background(), "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	if err != nil {
		t.Fatalf("GetNamespace: %v", err)
	}
	if ns.Description != nil {
		t.Errorf("expected nil Description, got %v", *ns.Description)
	}

	// success:false with no data must be reported as not-found, not as success.
	ts.responders["namespaces.get"] = map[string]any{"success": false, "message": "gone"}
	if _, err := c.GetNamespace(context.Background(), "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for success:false, got %v", err)
	}
}

func TestAPIKeyListEnvelopeDiffers(t *testing.T) {
	// apiKeys.list returns a bare {apiKeys:[...]} with no success flag, unlike
	// the other procedures. Decoding it as an envelope would silently yield zero
	// keys, so this is worth a test of its own.
	ts := newTestServer(t)
	ts.responders["apiKeys.list"] = map[string]any{
		"apiKeys": []map[string]any{{
			"uuid": "99999999-9999-9999-9999-999999999999", "name": "ci",
			"key": "mmcp_secret_value", "created_at": "2026-01-01",
			"is_active": true, "user_id": nil,
		}},
	}
	c := ts.client(t)
	keys, err := c.ListAPIKeys(context.Background())
	if err != nil {
		t.Fatalf("ListAPIKeys: %v", err)
	}
	if len(keys) != 1 || keys[0].Secret != "mmcp_secret_value" {
		t.Fatalf("unexpected keys: %+v", keys)
	}
}

func TestTRPCErrorSurfaces(t *testing.T) {
	ts := newTestServer(t)
	ts.errors["namespaces.list"] = map[string]any{
		"message": "You must be logged in to access this resource",
		"code":    -32001,
		"data":    map[string]any{"code": "UNAUTHORIZED", "httpStatus": 401},
	}
	c := ts.client(t)
	_, err := c.ListNamespaces(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "UNAUTHORIZED") {
		t.Errorf("error should name the tRPC code, got: %v", err)
	}
}

func TestSignInFailures(t *testing.T) {
	t.Run("rejected credentials", func(t *testing.T) {
		ts := newTestServer(t)
		ts.signInOK = false
		ts.signInStatus = http.StatusUnauthorized
		c := ts.client(t)
		err := c.SignIn(context.Background())
		if !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("expected ErrUnauthorized, got %v", err)
		}
		// The response body can reflect submitted input, so it must not leak
		// into the error text.
		if strings.Contains(err.Error(), "not-a-real-password") {
			t.Error("error message leaked the password")
		}
	})

	t.Run("rate limited", func(t *testing.T) {
		ts := newTestServer(t)
		ts.signInOK = false
		ts.signInStatus = http.StatusTooManyRequests
		c := ts.client(t)
		if err := c.SignIn(context.Background()); !errors.Is(err, ErrRateLimited) {
			t.Fatalf("expected ErrRateLimited, got %v", err)
		}
	})

	t.Run("no credentials at all", func(t *testing.T) {
		c, err := New(Config{Endpoint: "https://example.invalid"})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := c.SignIn(context.Background()); err == nil {
			t.Fatal("expected an error when no credentials are configured")
		}
	})
}

func TestStaticCookieSkipsSignIn(t *testing.T) {
	ts := newTestServer(t)
	ts.responders["namespaces.list"] = map[string]any{"success": true, "data": []any{}}
	ts.signInOK = false // proves sign-in is never attempted

	c, err := New(Config{Endpoint: ts.URL, Cookie: "provided-token"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.ListNamespaces(context.Background()); err != nil {
		t.Fatalf("a supplied cookie should skip sign-in entirely: %v", err)
	}
	if len(ts.calls) != 1 {
		t.Errorf("expected exactly one call, got %d", len(ts.calls))
	}
}

func TestNormalizeCookie(t *testing.T) {
	cases := map[string]string{
		"abc123":                           "better-auth.session_token=abc123",
		"better-auth.session_token=abc123": "better-auth.session_token=abc123",
	}
	for in, want := range cases {
		if got := normalizeCookie(in); got != want {
			t.Errorf("normalizeCookie(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEndpointValidation(t *testing.T) {
	// There is no built-in default: this repo is public and must not embed any
	// deployment's hostname, so an empty endpoint is an error, not a fallback.
	for _, bad := range []string{"example.com", "://noscheme", ""} {
		if _, err := New(Config{Endpoint: bad}); err == nil {
			t.Errorf("expected endpoint %q to be rejected", bad)
		}
	}
	c, err := New(Config{Endpoint: "https://example.com/"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if strings.HasSuffix(c.Endpoint(), "/") {
		t.Errorf("trailing slash not trimmed: %q", c.Endpoint())
	}
}

func TestEndpointRequired(t *testing.T) {
	_, err := New(Config{})
	if err == nil {
		t.Fatal("an empty endpoint must be an error, not a silent default")
	}
	if !strings.Contains(err.Error(), "endpoint is required") {
		t.Errorf("error should say the endpoint is required, got: %v", err)
	}
}
