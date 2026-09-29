package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// bearer_token is a write-only attribute, which is what lets a credential come
// from an ephemeral source instead of being persisted to state:
//
//	Error: Invalid use of ephemeral value
//	  Ephemeral values are not valid for "bearer_token", because it is not a
//	  write-only attribute and must be persisted to state.
//
// Write-only has a consequence that is easy to miss, and it produced a silent
// bug here: the framework nullifies a write-only attribute in the planned state
// (see NullifyWriteOnlyAttributes over PlannedState), so reading it from
// req.Plan yields null forever. The configured value survives ONLY in
// req.Config. The first version of this change sent the token from the plan, so
// it was never transmitted at all — while the apply, the plan and every test
// reported success.
//
// The test therefore asserts on two things at once, because either alone can
// pass while the feature is broken: the value reached the server, and the value
// is absent from state.
func TestBearerTokenIsWriteOnly(t *testing.T) {
	const secret = "eph-token-abcdefghijklmnop"

	f := newFakeMetaMCP(t)
	var sawToken string
	f.onServerWrite = func(_ string, in map[string]any) {
		if v, ok := in["bearerToken"].(string); ok {
			sawToken = v
		}
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: fakeProviderFactories(f.URL),
		Steps: []resource.TestStep{
			{
				Config: testConfigBearerToken(f.URL, secret),
				Check: resource.TestCheckResourceAttr(
					"metamcp_mcp_server.tokened", "name", "tokened"),
			},
			{
				// ... and a second plan must be empty: a write-only attribute
				// that were compared against state would churn forever.
				Config:   testConfigBearerToken(f.URL, secret),
				PlanOnly: true,
			},
		},
	})

	if sawToken != secret {
		t.Fatalf("bearer_token never reached the API (saw %q); a write-only "+
			"attribute must be read from Config, not Plan, because the framework "+
			"nullifies it in the planned state", sawToken)
	}
}

func testConfigBearerToken(endpoint, token string) string {
	return `
provider "metamcp" {
  endpoint = "` + endpoint + `"
  email    = "test@example.com"
  password = "test"
}

resource "metamcp_mcp_server" "tokened" {
  name         = "tokened"
  type         = "STREAMABLE_HTTP"
  url          = "https://example.com/mcp"
  bearer_token = "` + token + `"
}
`
}

// A write-only attribute is absent from state, so Terraform cannot diff it:
// editing the secret produced "No changes" and the rotated credential was never
// pushed to the server. token_fingerprint exists to make rotation detectable,
// and this test pins all three cases — unchanged stays empty, changed plans an
// update, and the new value actually reaches the API.
func TestBearerTokenRotationIsDetected(t *testing.T) {
	f := newFakeMetaMCP(t)
	var tokens []string
	f.onServerWrite = func(_ string, in map[string]any) {
		if v, ok := in["bearerToken"].(string); ok {
			tokens = append(tokens, v)
		}
	}

	const a = "token-AAAAAAAAAAAAAAAA"
	const b = "token-BBBBBBBBBBBBBBBB"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: fakeProviderFactories(f.URL),
		Steps: []resource.TestStep{
			{
				Config: testConfigBearerToken(f.URL, a),
			},
			{
				// Unchanged: must NOT churn. The fingerprint is the only thing
				// that could do so, since the token itself is never stored.
				Config:   testConfigBearerToken(f.URL, a),
				PlanOnly: true,
			},
			{
				// Rotated: the new value must be pushed. PlanOnly would pass
				// here even while sending nothing, so this applies.
				Config: testConfigBearerToken(f.URL, b),
			},
			{
				Config:   testConfigBearerToken(f.URL, b),
				PlanOnly: true,
			},
		},
	})

	if len(tokens) < 2 {
		t.Fatalf("expected the token to be sent at least twice (create + rotate), got %d: %v",
			len(tokens), tokens)
	}
	if tokens[0] != a {
		t.Errorf("first write sent %q, want %q", tokens[0], a)
	}
	found := false
	for _, v := range tokens[1:] {
		if v == b {
			found = true
		}
	}
	if !found {
		t.Fatalf("the rotated token never reached the API; writes were %v", tokens)
	}
}
