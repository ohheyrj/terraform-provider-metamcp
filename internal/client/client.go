// Package client is a minimal MetaMCP admin API client.
//
// MetaMCP has no documented admin API. Its web UI is a Next.js app backed by
// tRPC, and that tRPC surface — mounted at /trpc/frontend/* — *is* the admin
// API. This package speaks it directly.
//
// # Authentication
//
// There are two entirely separate auth systems, and conflating them is the
// usual source of confusion:
//
//	Admin tRPC    /trpc/frontend/*            better-auth session cookie only
//	MCP gateway   /metamcp/<endpoint>/mcp     API key or OAuth
//
// Every admin procedure is a `protectedProcedure` whose context is built solely
// from the request's `cookie` header (see the server's createContext). There is
// no Bearer-token path: better-auth is configured without its bearer plugin, so
// an `Authorization` header is not merely rejected, it is never read. An API
// key therefore cannot call the admin API — it only consumes the MCP gateway.
//
// A session is obtained by posting credentials to /api/auth/sign-in/email
// (better-auth) and keeping the resulting cookie. Sessions last 7 days and
// slide forward on use (updateAge: 1 day).
//
// # Wire format
//
// tRPC v11 over HTTP. A batch of one is wrapped as {"0":{"json":<input>}} and
// answered as [{"result":{"data":...}}]. Queries must use GET with the input
// URL-encoded into ?input=; mutations must POST it as the body. Sending a query
// via POST returns 405, which is why Call() picks the verb by procedure name.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultEndpoint is intentionally empty: this repository is public and must
// not embed any particular deployment's hostname. An endpoint is required
// configuration — set one explicitly, or via METAMCP_ENDPOINT.
const DefaultEndpoint = ""

// Sentinel errors callers may match on.
var (
	// ErrUnauthorized means the session cookie was missing, expired or invalid.
	ErrUnauthorized = errors.New("metamcp: unauthorized (session invalid or expired)")

	// ErrRateLimited means better-auth throttled the sign-in endpoint. It is
	// reported separately because a 429 on login otherwise reads as "wrong
	// password", which sends you looking in the wrong place.
	ErrRateLimited = errors.New("metamcp: rate limited (too many sign-in attempts)")

	// ErrNotFound means the requested object does not exist.
	ErrNotFound = errors.New("metamcp: not found")
)

// Config configures a Client.
type Config struct {
	// Endpoint is the MetaMCP base URL, e.g. https://metamcp.example.com.
	Endpoint string

	// Email and Password authenticate the session. MetaMCP signs in by email
	// address only: better-auth runs without its username plugin and the users
	// table has no username column, so a bare username cannot sign in.
	Email    string
	Password string

	// Cookie, if set, is used verbatim instead of signing in. This is the
	// preferred mode for interactive use: it is a 7-day sliding session that
	// can be revoked, whereas a stored password is a long-lived credential.
	// Accepts either a bare token or a full `name=value` pair.
	Cookie string

	// HTTPClient is optional; a sensible default is used when nil.
	HTTPClient *http.Client

	// UserAgent is sent with every request.
	UserAgent string
}

// Client is a MetaMCP admin API client. It is safe for concurrent use.
type Client struct {
	endpoint   string
	email      string
	password   string
	staticCook string
	userAgent  string
	http       *http.Client

	mu       sync.Mutex
	signedIn bool
}

// New builds a Client from cfg.
func New(cfg Config) (*Client, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("metamcp: endpoint is required (set endpoint or METAMCP_ENDPOINT)")
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("metamcp: invalid endpoint %q: %w", cfg.Endpoint, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("metamcp: endpoint %q must include scheme and host", cfg.Endpoint)
	}

	hc := cfg.HTTPClient
	if hc == nil {
		jar, err := cookiejar.New(nil)
		if err != nil {
			return nil, fmt.Errorf("metamcp: creating cookie jar: %w", err)
		}
		hc = &http.Client{Timeout: 30 * time.Second, Jar: jar}
	} else if hc.Jar == nil {
		// A caller-supplied client still needs a jar, or the session cookie
		// returned by the login response is dropped and every later call is 401.
		jar, err := cookiejar.New(nil)
		if err != nil {
			return nil, fmt.Errorf("metamcp: creating cookie jar: %w", err)
		}
		hc.Jar = jar
	}

	ua := cfg.UserAgent
	if ua == "" {
		ua = "terraform-provider-metamcp"
	}

	return &Client{
		endpoint:   strings.TrimRight(cfg.Endpoint, "/"),
		email:      cfg.Email,
		password:   cfg.Password,
		staticCook: cfg.Cookie,
		userAgent:  ua,
		http:       hc,
	}, nil
}

// SignIn authenticates and stores the session cookie in the client's jar. It is
// called lazily by the first request, and can be called explicitly to surface
// credential errors at configuration time rather than mid-apply.
func (c *Client) SignIn(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.staticCook != "" {
		// Nothing to do: the caller supplied a session directly.
		return nil
	}
	if c.email == "" || c.password == "" {
		return errors.New("metamcp: no credentials supplied: set email and password, or cookie")
	}

	// A failed login response can reflect the submitted input, so the body is
	// never included in an error — only the status.
	body, err := json.Marshal(map[string]string{
		"email":    c.email,
		"password": c.password,
	})
	if err != nil {
		return fmt.Errorf("metamcp: encoding credentials: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.endpoint+"/api/auth/sign-in/email", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("metamcp: building sign-in request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("metamcp: sign-in request failed: %w", err)
	}
	// Body is drained so the connection can be reused, then closed. The close
	// error is discarded deliberately: the response is already fully read and
	// nothing downstream depends on it.
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	switch resp.StatusCode {
	case http.StatusOK:
		// ok
	case http.StatusTooManyRequests:
		return ErrRateLimited
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: sign-in rejected (HTTP %d)", ErrUnauthorized, resp.StatusCode)
	default:
		return fmt.Errorf("metamcp: sign-in failed (HTTP %d)", resp.StatusCode)
	}

	c.signedIn = true
	return nil
}

// tRPCError is an error returned by the server in a tRPC envelope.
type tRPCError struct {
	Message    string `json:"message"`
	Code       int    `json:"code"`
	HTTPStatus int    `json:"httpStatus"`
	Data       struct {
		Code       string `json:"code"`
		HTTPStatus int    `json:"httpStatus"`
		Path       string `json:"path"`
	} `json:"data"`
}

func (e *tRPCError) Error() string {
	if e.Data.Code != "" {
		return fmt.Sprintf("metamcp: %s: %s", e.Data.Code, e.Message)
	}
	return "metamcp: " + e.Message
}

// isMutation reports whether a procedure name denotes a mutation, which must be
// sent as a POST. Queries sent via POST return 405, so this decision cannot be
// skipped. The list is derived from the MetaMCP router definitions.
func isMutation(proc string) bool {
	verb := proc[strings.LastIndex(proc, ".")+1:]
	switch verb {
	case "create", "update", "delete", "bulkImport", "sync", "clear",
		"refreshTools", "updateServerStatus", "updateToolStatus",
		"updateToolOverrides", "upsert":
		return true
	}
	// Config setters are all named set*.
	return strings.HasPrefix(verb, "set")
}

// callRaw performs a single batched tRPC call and returns the raw `data`
// member, leaving interpretation to the typed wrappers below.
func (c *Client) callRaw(ctx context.Context, proc string, input any, out any) error {
	if err := c.SignIn(ctx); err != nil {
		return err
	}

	raw, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("metamcp: encoding input for %s: %w", proc, err)
	}

	// Batch-of-one envelope: {"0":{"json":<input>}}
	envelope := fmt.Sprintf(`{"0":{"json":%s}}`, raw)

	var req *http.Request
	if isMutation(proc) {
		req, err = http.NewRequestWithContext(ctx, http.MethodPost,
			c.endpoint+"/trpc/frontend."+proc+"?batch=1", strings.NewReader(envelope))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
		}
	} else {
		req, err = http.NewRequestWithContext(ctx, http.MethodGet,
			c.endpoint+"/trpc/frontend."+proc+"?batch=1&input="+url.QueryEscape(envelope), nil)
	}
	if err != nil {
		return fmt.Errorf("metamcp: building request for %s: %w", proc, err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	if c.staticCook != "" {
		req.Header.Set("Cookie", normalizeCookie(c.staticCook))
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("metamcp: request to %s failed: %w", proc, err)
	}
	defer func() { _ = resp.Body.Close() }()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("metamcp: reading response from %s: %w", proc, err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return fmt.Errorf("%w (procedure %s)", ErrUnauthorized, proc)
	case http.StatusNotFound:
		return fmt.Errorf("%w (procedure %s)", ErrNotFound, proc)
	case http.StatusTooManyRequests:
		return ErrRateLimited
	default:
		return fmt.Errorf("metamcp: %s returned HTTP %d: %s",
			proc, resp.StatusCode, truncate(string(payload), 300))
	}

	// Answer shape is [{ "result": { "data": ... } }] or [{ "error": {...} }].
	var batch []struct {
		Result *struct {
			Data json.RawMessage `json:"data"`
		} `json:"result"`
		Error *tRPCError `json:"error"`
	}
	if err := json.Unmarshal(payload, &batch); err != nil {
		return fmt.Errorf("metamcp: decoding response from %s: %w (body: %s)",
			proc, err, truncate(string(payload), 300))
	}
	if len(batch) == 0 {
		return fmt.Errorf("metamcp: empty batch response from %s", proc)
	}
	if batch[0].Error != nil {
		return batch[0].Error
	}
	if batch[0].Result == nil {
		return fmt.Errorf("metamcp: response from %s had neither result nor error", proc)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(batch[0].Result.Data, out); err != nil {
		return fmt.Errorf("metamcp: decoding data from %s: %w", proc, err)
	}
	return nil
}

// Call invokes an arbitrary frontend procedure. Useful for procedures without a
// typed wrapper, and for the escape hatch in tests.
func (c *Client) Call(ctx context.Context, proc string, input, out any) error {
	return c.callRaw(ctx, proc, input, out)
}

// Endpoint returns the configured base URL, without a trailing slash. Resources
// use it to derive client-facing URLs.
func (c *Client) Endpoint() string { return c.endpoint }

// normalizeCookie accepts either a bare session token or a full cookie pair and
// returns a valid Cookie header value.
func normalizeCookie(v string) string {
	if strings.Contains(v, "=") {
		return v
	}
	return "better-auth.session_token=" + v
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
