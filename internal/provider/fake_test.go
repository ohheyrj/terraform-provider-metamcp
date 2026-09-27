package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// fakeMetaMCP is a small stateful MetaMCP: enough of the admin API to exercise
// real resource CRUD and import through the Terraform plugin protocol.
//
// The unit tests in internal/client assert the wire format; these exist to prove
// the provider's own Create/Read/Update/Delete and ImportState wiring works
// against the framework. That layer was previously untested, and import bugs
// show up there rather than in the client.
//
// It deliberately reproduces the awkward parts of the real API:
//   - responses are wrapped in {success, data, message}
//   - namespaces.get returns servers, namespaces.list does not
//   - update replaces mcpServerUuids wholesale
//   - deletes report failure inside a 200 response body
type fakeMetaMCP struct {
	*httptest.Server

	mu         sync.Mutex
	namespaces map[string]map[string]any
	servers    map[string]map[string]any
	endpoints  map[string]map[string]any
	apiKeys    map[string]map[string]any
	seq        int
}

// TestMain opts the whole provider package into the acceptance-test harness.
// These tests are the usual resource.Test kind, but they run against an
// in-process fake rather than a live MetaMCP: no network, no credentials. The
// harness has no way to know that, so it is told here, once, instead of each
// test setting and unsetting the variable.
func TestMain(m *testing.M) {
	if os.Getenv("TF_ACC") == "" {
		_ = os.Setenv("TF_ACC", "1")
	}
	os.Exit(m.Run())
}

func newFakeMetaMCP(t *testing.T) *fakeMetaMCP {
	t.Helper()
	f := &fakeMetaMCP{
		namespaces: map[string]map[string]any{},
		servers:    map[string]map[string]any{},
		endpoints:  map[string]map[string]any{},
		apiKeys:    map[string]map[string]any{},
	}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/auth/sign-in/email", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "better-auth.session_token", Value: "fake", Path: "/"})
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{"email": "x"}})
	})

	mux.HandleFunc("/trpc/", func(w http.ResponseWriter, r *http.Request) {
		proc := strings.TrimPrefix(r.URL.Path, "/trpc/frontend.")

		// Queries carry the envelope URL-encoded in ?input=, mutations carry it
		// as the POST body. Parsing only the body would make every .get call
		// look like it had no uuid, and the provider would then (correctly)
		// treat the resource as deleted.
		var raw string
		if r.Method == http.MethodGet {
			raw = r.URL.Query().Get("input")
		} else {
			buf, _ := io.ReadAll(r.Body)
			raw = string(buf)
		}

		var input map[string]any
		if raw != "" {
			var env map[string]struct {
				JSON json.RawMessage `json:"json"`
			}
			if err := json.Unmarshal([]byte(raw), &env); err == nil {
				if e, ok := env["0"]; ok && len(e.JSON) > 0 {
					_ = json.Unmarshal(e.JSON, &input)
				}
			}
		}

		f.mu.Lock()
		defer f.mu.Unlock()

		data, err := f.handle(proc, input)
		if err != nil {
			_ = json.NewEncoder(w).Encode([]any{map[string]any{
				"error": map[string]any{
					"message": err.Error(), "code": -32600,
					"data": map[string]any{"code": "BAD_REQUEST", "path": proc},
				},
			}})
			return
		}
		_ = json.NewEncoder(w).Encode([]any{map[string]any{
			"result": map[string]any{"data": data},
		}})
	})

	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeMetaMCP) nextUUID() string {
	f.seq++
	return fmt.Sprintf("%08d-0000-0000-0000-%012d", f.seq, f.seq)
}

func (f *fakeMetaMCP) handle(proc string, in map[string]any) (any, error) {
	str := func(k string) string {
		if v, ok := in[k].(string); ok {
			return v
		}
		return ""
	}
	uuids := func(k string) []string {
		raw, ok := in[k].([]any)
		if !ok {
			return nil
		}
		out := make([]string, 0, len(raw))
		for _, v := range raw {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}

	switch proc {
	case "namespaces.list":
		list := []any{}
		for _, ns := range f.namespaces {
			list = append(list, ns)
		}
		return map[string]any{"success": true, "data": list}, nil

	case "namespaces.get":
		ns, ok := f.namespaces[str("uuid")]
		if !ok {
			return map[string]any{"success": false, "message": "Namespace not found"}, nil
		}
		// get returns the namespace WITH its servers, unlike list.
		out := map[string]any{}
		for k, v := range ns {
			out[k] = v
		}
		servers := []any{}
		if ids, ok := ns["_servers"].([]string); ok {
			for _, id := range ids {
				if srv, ok := f.servers[id]; ok {
					servers = append(servers, srv)
				}
			}
		}
		out["servers"] = servers
		delete(out, "_servers")
		return map[string]any{"success": true, "data": out}, nil

	case "namespaces.create":
		id := f.nextUUID()
		ns := map[string]any{
			"uuid": id, "name": str("name"),
			"description": in["description"],
			"created_at":  "2026-01-01T00:00:00Z",
			"updated_at":  "2026-01-01T00:00:00Z",
			"user_id":     nil,
			"_servers":    uuids("mcpServerUuids"),
		}
		f.namespaces[id] = ns
		return map[string]any{"success": true, "data": f.publicNamespace(ns)}, nil

	case "namespaces.update":
		id := str("uuid")
		ns, ok := f.namespaces[id]
		if !ok {
			return map[string]any{"success": false, "message": "Namespace not found"}, nil
		}
		ns["name"] = str("name")
		ns["description"] = in["description"]
		// update replaces the association wholesale.
		ns["_servers"] = uuids("mcpServerUuids")
		ns["updated_at"] = "2026-01-02T00:00:00Z"
		return map[string]any{"success": true, "data": f.publicNamespace(ns)}, nil

	case "namespaces.delete":
		delete(f.namespaces, str("uuid"))
		return map[string]any{"success": true, "message": "ok"}, nil

	case "mcpServers.list":
		list := []any{}
		for _, s := range f.servers {
			list = append(list, s)
		}
		return map[string]any{"success": true, "data": list}, nil

	case "mcpServers.get":
		srv, ok := f.servers[str("uuid")]
		if !ok {
			return map[string]any{"success": false, "message": "Server not found"}, nil
		}
		return map[string]any{"success": true, "data": srv}, nil

	case "mcpServers.create":
		id := f.nextUUID()
		srv := map[string]any{
			"uuid": id, "name": str("name"), "description": in["description"],
			"type": str("type"), "command": in["command"],
			"args":        sliceOrEmpty(in["args"]),
			"env":         mapOrEmpty(in["env"]),
			"url":         in["url"],
			"bearerToken": in["bearerToken"],
			"headers":     mapOrEmpty(in["headers"]),
			"created_at":  "2026-01-01T00:00:00Z",
			"user_id":     nil,
		}
		f.servers[id] = srv
		return map[string]any{"success": true, "data": srv}, nil

	case "mcpServers.update":
		id := str("uuid")
		srv, ok := f.servers[id]
		if !ok {
			return map[string]any{"success": false, "message": "Server not found"}, nil
		}
		srv["name"] = str("name")
		srv["type"] = str("type")
		srv["url"] = in["url"]
		return map[string]any{"success": true, "data": srv}, nil

	case "mcpServers.delete":
		delete(f.servers, str("uuid"))
		return map[string]any{"success": true, "message": "ok"}, nil

	case "endpoints.list":
		list := []any{}
		for _, e := range f.endpoints {
			list = append(list, e)
		}
		return map[string]any{"success": true, "data": list}, nil

	case "endpoints.get":
		e, ok := f.endpoints[str("uuid")]
		if !ok {
			return map[string]any{"success": false, "message": "Endpoint not found"}, nil
		}
		// get returns the endpoint WITH its namespace embedded.
		out := map[string]any{}
		for k, v := range e {
			out[k] = v
		}
		nsID, _ := e["namespace_uuid"].(string)
		if ns, ok := f.namespaces[nsID]; ok {
			out["namespace"] = f.publicNamespace(ns)
		}
		return map[string]any{"success": true, "data": out}, nil

	case "endpoints.create":
		id := f.nextUUID()
		e := map[string]any{
			"uuid": id, "name": str("name"), "description": in["description"],
			"namespace_uuid":       str("namespaceUuid"),
			"enable_api_key_auth":  boolOr(in["enableApiKeyAuth"], true),
			"enable_oauth":         boolOr(in["enableOauth"], false),
			"use_query_param_auth": boolOr(in["useQueryParamAuth"], false),
			"created_at":           "2026-01-01T00:00:00Z",
			"updated_at":           "2026-01-01T00:00:00Z",
			"user_id":              nil,
		}
		f.endpoints[id] = e
		return map[string]any{"success": true, "data": e}, nil

	case "endpoints.update":
		id := str("uuid")
		e, ok := f.endpoints[id]
		if !ok {
			return map[string]any{"success": false, "message": "Endpoint not found"}, nil
		}
		e["name"] = str("name")
		e["updated_at"] = "2026-01-02T00:00:00Z"
		return map[string]any{"success": true, "data": e}, nil

	case "endpoints.delete":
		delete(f.endpoints, str("uuid"))
		return map[string]any{"success": true, "message": "ok"}, nil

	case "apiKeys.list":
		// Odd one out: this procedure returns {apiKeys:[...]} with no envelope.
		list := []any{}
		for _, k := range f.apiKeys {
			list = append(list, k)
		}
		return map[string]any{"apiKeys": list}, nil

	case "apiKeys.create":
		id := f.nextUUID()
		// The key value is returned once, at creation, and never again.
		k := map[string]any{
			"uuid": id, "name": str("name"),
			"key":        "fake-key-" + id,
			"user_id":    nil,
			"created_at": "2026-01-01T00:00:00Z",
			"is_active":  true,
		}
		f.apiKeys[id] = k
		// Bare, like the real API: apiKeys.create returns
		// CreateApiKeyResponseSchema directly with no {success,data} envelope.
		// (apiKeys.list is the other odd one out.)
		return map[string]any{
			"uuid": k["uuid"], "name": k["name"], "key": k["key"],
			"created_at": k["created_at"],
		}, nil

	case "apiKeys.get":
		k, ok := f.apiKeys[str("uuid")]
		if !ok {
			return map[string]any{"success": false, "message": "API key not found"}, nil
		}
		return map[string]any{"success": true, "data": k}, nil

	case "apiKeys.update":
		id := str("uuid")
		k, ok := f.apiKeys[id]
		if !ok {
			return map[string]any{"success": false, "message": "API key not found"}, nil
		}
		k["name"] = str("name")
		if v, ok := in["isActive"].(bool); ok {
			k["is_active"] = v
		}
		return map[string]any{"success": true, "data": k}, nil

	case "apiKeys.delete":
		delete(f.apiKeys, str("uuid"))
		return map[string]any{"success": true, "message": "ok"}, nil
	}

	return nil, fmt.Errorf("fake: unhandled procedure %s", proc)
}

// publicNamespace strips the internal _servers key, matching the real API which
// never exposes it.
func (f *fakeMetaMCP) publicNamespace(ns map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range ns {
		if k == "_servers" {
			continue
		}
		out[k] = v
	}
	return out
}

func sliceOrEmpty(v any) any {
	if v == nil {
		return []any{}
	}
	return v
}

func boolOr(v any, def bool) any {
	if b, ok := v.(bool); ok {
		return b
	}
	return def
}

func mapOrEmpty(v any) any {
	if v == nil {
		return map[string]any{}
	}
	return v
}
