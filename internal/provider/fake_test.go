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
//
// fakeUserID is the authenticated user the fake reports, and therefore the owner
// it expects on anything made private.
const fakeUserID = "user-0000-1111-2222-3333"

// otherUserID stands in for a second, unrelated user. It exists so a test can ask
// the question visibility actually answers — can somebody else use this object?
// — rather than only inspecting the stored owner column.
const otherUserID = "user-9999-8888-7777-6666"

type fakeMetaMCP struct {
	*httptest.Server

	mu         sync.Mutex
	namespaces map[string]map[string]any
	servers    map[string]map[string]any
	endpoints  map[string]map[string]any
	apiKeys    map[string]map[string]any
	seq        int

	// onServerWrite, when set, receives the input of every create/update so a
	// test can assert on what actually reached the API — necessary for
	// write-only attributes, whose value is absent from the plan and state.
	onServerWrite func(proc string, in map[string]any)

	// onEndpointWrite does the same for endpoints. Endpoint visibility has the
	// same property: what matters is the ownership actually sent, not what
	// Terraform recorded.
	onEndpointWrite func(proc string, in map[string]any)

	// onAPIKeyWrite does the same for API keys. It matters here more than
	// anywhere: an API key's ownership is settable ONLY at creation, so "what
	// did the create request actually carry" is the only evidence that
	// is_public did anything at all.
	onAPIKeyWrite func(proc string, in map[string]any)
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
		// Shape as better-auth returns it, including user.id: the provider needs
		// the real id to make an object private, since the API stores user_id
		// verbatim rather than resolving it server-side.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"user": map[string]any{"id": fakeUserID, "email": "test@example.com"},
		})
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

		// Mirror tRPC's batch rule rather than accommodating whatever the client
		// sends. The body must be Record<batchIndex, input>: the index is
		// required, and the inner `json` key is a transformer slot that must be
		// ABSENT here because MetaMCP configures no transformer. A fake that
		// accepted either shape would agree with the client and hide exactly the
		// bug this reproduces.
		var input map[string]any
		if raw != "" {
			var batch map[string]json.RawMessage
			if err := json.Unmarshal([]byte(raw), &batch); err != nil {
				f.fail(w, fmt.Sprintf("input is not a batch object: %v", err))
				return
			}
			entry, ok := batch["0"]
			if !ok {
				f.fail(w, "batch index 0 missing (tRPC requires Record<index, input> with batch=1)")
				return
			}
			var probe map[string]json.RawMessage
			if json.Unmarshal(entry, &probe) == nil {
				if _, wrapped := probe["json"]; wrapped {
					f.fail(w, "input is superjson-wrapped but the server configures no transformer")
					return
				}
			}
			if err := json.Unmarshal(entry, &input); err != nil {
				f.fail(w, fmt.Sprintf("decoding input: %v", err))
				return
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

// fail rejects a request the way the real server does, so an invalid wire format
// surfaces as the same 400 a live instance would produce instead of being
// silently accommodated.
func (f *fakeMetaMCP) fail(w http.ResponseWriter, why string) {
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode([]any{map[string]any{
		"error": map[string]any{
			"message": why, "code": -32600,
			"data": map[string]any{"code": "BAD_REQUEST", "httpStatus": 400},
		},
	}})
}

func (f *fakeMetaMCP) nextUUID() string {
	f.seq++
	return fmt.Sprintf("%08d-0000-0000-0000-%012d", f.seq, f.seq)
}

func (f *fakeMetaMCP) handle(proc string, in map[string]any) (any, error) {
	if f.onServerWrite != nil && (proc == "mcpServers.create" || proc == "mcpServers.update") {
		f.onServerWrite(proc, in)
	}
	if f.onEndpointWrite != nil && proc == "endpoints.create" {
		f.onEndpointWrite(proc, in)
	}
	if f.onAPIKeyWrite != nil && (proc == "apiKeys.create" || proc == "apiKeys.update") {
		f.onAPIKeyWrite(proc, in)
	}
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
			"user_id":     nsOwner(in),
			"_servers":    uuids("mcpServerUuids"),
		}
		if msg := f.checkNamespaceServerVisibility(ns["user_id"], uuids("mcpServerUuids")); msg != "" {
			return map[string]any{"success": false, "message": msg}, nil
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
		if v, ok := in["description"]; ok {
			ns["description"] = v
		}
		// The API spreads the input over the update, so an absent key keeps the
		// existing owner while an explicit null makes the namespace public.
		if v, ok := in["user_id"]; ok {
			ns["user_id"] = ownerFor(v)
		}
		// update replaces the association wholesale.
		ns["_servers"] = uuids("mcpServerUuids")
		if msg := f.checkNamespaceServerVisibility(ns["user_id"], uuids("mcpServerUuids")); msg != "" {
			return map[string]any{"success": false, "message": msg}, nil
		}
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
			// Owned by the authenticated user unless the request clears it, which
			// is how the real API records visibility.
			"user_id": ownerFor(in["user_id"]),
			// Present on every server the serializer returns.
			"error_status": "NONE",
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
		// Every optional field follows the real API rule: an ABSENT key means
		// "keep the existing value", a present one replaces it. The API's request
		// schemas are `z.string().optional()` and its repository spread is
		// `{uuid, ...updateData}`, so that is literally what happens.
		//
		// This has to be modelled rather than assumed. The previous version
		// hardcoded `srv["url"] = in["url"]`, which copies nil over the stored
		// value, and never touched description at all — so a provider that
		// omitted a field (via `omitempty` on a nil pointer) would have its own
		// mistake echoed back as agreement, and the
		// "was null, but now cty.StringVal(...)" failure could never reproduce
		// here even though it happens against the real server.
		for _, k := range []string{
			"description", "url", "command", "bearerToken", "args", "env", "headers",
		} {
			if v, ok := in[k]; ok {
				srv[k] = v
			}
		}
		// Ownership follows the same rule: an ABSENT user_id keeps the existing
		// owner, while an explicit null clears it. Distinguishing the two is the
		// whole point of visibility, so a fake that ignored user_id here would
		// hide a real bug — and one that treated absent as null would make every
		// update public.
		if v, ok := in["user_id"]; ok {
			srv["user_id"] = ownerFor(v)
		}
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
			// Ownership: an ABSENT key defaults to the authenticated user, an
			// explicit null is public. Same rule as a namespace (nsOwner).
			"user_id": nsOwner(in),
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
		e["namespace_uuid"] = str("namespaceUuid")
		// The real update passes description straight through to the repository.
		if v, ok := in["description"]; ok {
			e["description"] = v
		}
		e["updated_at"] = "2026-01-02T00:00:00Z"
		// user_id is DELIBERATELY ignored here, mirroring the real API: its
		// update procedure never passes the ownership field to the repository,
		// so ownership is fixed at creation. A fake that honoured it would make
		// an in-place visibility change look like it worked, while the live
		// instance silently kept the old owner — and the resulting
		// "inconsistent result after apply" could never reproduce here.
		return map[string]any{"success": true, "data": e}, nil

	case "endpoints.delete":
		delete(f.endpoints, str("uuid"))
		return map[string]any{"success": true, "message": "ok"}, nil

	case "apiKeys.list":
		// Odd one out: this procedure returns {apiKeys:[...]} with no envelope.
		// It is also the ONLY procedure that reports ownership for a key —
		// neither create nor update returns a user_id field — which is what
		// makes the provider's read path the only correct source for is_public.
		list := []any{}
		for _, k := range f.apiKeys {
			list = append(list, k)
		}
		return map[string]any{"apiKeys": list}, nil

	case "apiKeys.create":
		id := f.nextUUID()
		// The key value is returned once, at creation, and never again.
		//
		// Ownership follows api-keys.impl.ts exactly: `input.user_id !== undefined
		// ? input.user_id : userId` — the same rule the namespace and endpoint
		// create paths use. An ABSENT ownership key claims the caller, and an
		// explicit null makes the key public.
		//
		// A fake that defaulted an absent key to nil would make an omitted
		// is_public look public, when the real API stores it private; and one
		// that refused the null would hide the fact that public keys are
		// creatable at all.
		owner := apiKeyOwner(in)
		k := map[string]any{
			"uuid": id, "name": str("name"),
			"key":        "fake-key-" + id,
			"user_id":    owner,
			"created_at": "2026-01-01T00:00:00Z",
			"is_active":  boolOr(in["is_active"], true),
		}
		f.apiKeys[id] = k
		// Bare, like the real API: apiKeys.create returns
		// CreateApiKeyResponseSchema ({uuid, name, key, created_at}) directly,
		// with no {success,data} envelope and no ownership field.
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
		// UpdateApiKeyRequestSchema is {uuid, name?, is_active?}. Every optional
		// field follows the API's rule — absent means keep, present replaces —
		// so a provider that omitted the name must not have the stored one
		// overwritten with "".
		//
		// There is deliberately NO ownership handling: the real update procedure
		// never passes user_id to the repository, so a provider that tried to
		// change a key's visibility in place would have its mistake silently
		// ignored against the live server. Modelling that here is what makes the
		// in-place failure reproducible locally instead of only in production.
		if v, ok := in["name"]; ok {
			k["name"] = v
		}
		if v, ok := in["isActive"].(bool); ok {
			k["is_active"] = v
		}
		// UpdateApiKeyResponseSchema DOES include the key, but not user_id. The
		// response is built as a separate map so that omitting or reshaping a
		// field here can never mutate what the fake has stored.
		//
		// Returned BARE, with no {success,data} wrapper: the real procedure's
		// `.output(UpdateApiKeyResponseSchema)` is a plain object, exactly like
		// apiKeys.create. This was previously wrapped, which meant every update
		// decoded to an empty uuid and the client reported "not found" — a bug
		// that lived in the fake and hid a genuine provider failure.
		return map[string]any{
			"uuid": k["uuid"], "name": k["name"], "key": k["key"],
			"created_at": k["created_at"], "is_active": k["is_active"],
		}, nil

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

// ownerFor mirrors how the API records visibility: null user_id means public, a
// user id means that owner, and an absent key means "leave it".
//
// An empty string is deliberately NOT accepted as public. user_id is a foreign
// key to users.id, so the real database rejects "" as a constraint violation —
// accepting it here (as an earlier version did) let a provider that sent "" for
// public pass every test while failing against the real server.
func ownerFor(v any) any {
	if v == nil {
		return nil
	}
	if s, ok := v.(string); ok {
		if s == "" {
			panic("fake: user_id was sent as an empty string, which is a foreign-key " +
				"violation in the real schema; public must be an explicit null")
		}
		return s
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

// seedServer inserts a server as though it already existed on the real instance
// — for example one created through the UI, whose optional fields the Terraform
// configuration never declared. It returns the uuid for use with ImportState.
func (f *fakeMetaMCP) seedServer(fields map[string]any) string {
	id := f.nextUUID()
	srv := map[string]any{
		"uuid":         id,
		"name":         "",
		"description":  nil,
		"type":         "STREAMABLE_HTTP",
		"url":          nil,
		"command":      nil,
		"args":         []any{},
		"env":          map[string]any{},
		"headers":      map[string]any{},
		"bearerToken":  nil,
		"error_status": "NONE",
		"user_id":      nil,
		"created_at":   "2026-09-20T00:00:00Z",
	}
	for k, v := range fields {
		srv[k] = v
	}
	f.servers[id] = srv
	return id
}

// serverField reads a stored field, so a test can assert what actually reached
// the server rather than only what Terraform recorded in state.
func (f *fakeMetaMCP) serverField(id, field string) any {
	return f.servers[id][field]
}

// checkStoredAPIKeyOwner asserts the owner the fake has on file for a key.
//
// It reads the STORED record rather than any response body, which is what makes
// it able to catch a provider that took ownership from the create response: the
// storage path is set from the request, so the two cannot agree by accident.
func (f *fakeMetaMCP) checkStoredAPIKeyOwner(uuid string, wantPublic bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k, ok := f.apiKeys[uuid]
	if !ok {
		return fmt.Errorf("no API key stored under uuid %s", uuid)
	}
	owner := k["user_id"]
	if wantPublic && owner != nil {
		return fmt.Errorf("key %s has owner %#v, want no owner (public)", uuid, owner)
	}
	if !wantPublic && owner == nil {
		return fmt.Errorf("key %s has no owner, want one (private)", uuid)
	}
	return nil
}

// checkAPIKeyAccess reports whether a key would be returned to a DIFFERENT user,
// mirroring the API's own findAccessibleToUser rule: public keys (no owner) plus
// the caller's own. This is the access consequence of visibility, as opposed to
// the stored column.
func (f *fakeMetaMCP) checkAPIKeyAccess(uuid string, wantVisible bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k, ok := f.apiKeys[uuid]
	if !ok {
		return fmt.Errorf("no API key stored under uuid %s", uuid)
	}
	visible := k["user_id"] == nil || k["user_id"] == otherUserID
	if visible != wantVisible {
		return fmt.Errorf("key %s visible to other user = %v, want %v (owner %#v)",
			uuid, visible, wantVisible, k["user_id"])
	}
	return nil
}

// nsOwner mirrors the API's create rule: `input.user_id !== undefined ?
// input.user_id : userId`. An absent key defaults to the authenticated user, an
// explicit null makes the namespace public.
func nsOwner(in map[string]any) any {
	if v, ok := in["user_id"]; ok {
		return ownerFor(v)
	}
	return fakeUserID
}

// apiKeyOwner mirrors apiKeys.create's own resolution.
//
// It is the SAME rule as nsOwner, and that is worth stating because it is easy
// to assume otherwise: api-keys.impl.ts computes exactly `input.user_id !==
// undefined ? input.user_id : userId`, so an explicit null really does create a
// public key. An absent key claims the caller.
func apiKeyOwner(in map[string]any) any {
	return nsOwner(in)
}

// checkNamespaceServerVisibility enforces the API's relationship rule: a public
// namespace may only contain public servers. The real server refuses with
// "Access denied: Public namespaces can only contain public MCP servers. Server
// \"<name>\" is private", so the fake must refuse too — otherwise a provider that
// attached a private server to a public namespace would look successful here and
// fail only against the live instance.
func (f *fakeMetaMCP) checkNamespaceServerVisibility(owner any, serverUUIDs []string) string {
	if owner != nil {
		return "" // private namespace: no restriction
	}
	for _, uuid := range serverUUIDs {
		srv, ok := f.servers[uuid]
		if !ok {
			continue
		}
		if srv["user_id"] != nil {
			name, _ := srv["name"].(string)
			return "Access denied: Public namespaces can only contain public MCP " +
				"servers. Server \"" + name + "\" is private"
		}
	}
	return ""
}
