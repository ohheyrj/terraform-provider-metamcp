package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// These tests call the plan modifier directly rather than through a Terraform
// run, because the framework-level plan checks cannot see this distinction.
//
// That is worth stating explicitly, since it is the opposite of what one would
// assume: a resource.Test step asserts `ExpectResourceAction(Update)` for an
// unrelated edit, and that assertion still passes when the modifier has been made
// to fire unconditionally — the framework does not surface a spurious
// replacement for an unchanged attribute in that path. So the counterweight tests
// in api_key_visibility_test.go and endpoint_visibility_test.go pin the *outcome*
// (a rename does not rotate a credential, a description edit does not replace an
// endpoint) but they do NOT pin the modifier's narrowness. This file does, and
// the two are complementary rather than redundant.

// rawAPIKey renders a whole object with only is_public populated.
func rawAPIKey(isPublic tftypes.Value) tftypes.Value {
	nullStr := tftypes.NewValue(tftypes.String, nil)
	return tftypes.NewValue(tftypes.Object{AttributeTypes: map[string]tftypes.Type{
		"uuid":       tftypes.String,
		"name":       tftypes.String,
		"key":        tftypes.String,
		"is_active":  tftypes.Bool,
		"is_public":  tftypes.Bool,
		"created_at": tftypes.String,
	}}, map[string]tftypes.Value{
		"uuid":       nullStr,
		"name":       nullStr,
		"key":        nullStr,
		"is_active":  tftypes.NewValue(tftypes.Bool, nil),
		"is_public":  isPublic,
		"created_at": nullStr,
	})
}

func nullWholeObject() tftypes.Value {
	return tftypes.NewValue(tftypes.Object{AttributeTypes: map[string]tftypes.Type{
		"uuid":       tftypes.String,
		"name":       tftypes.String,
		"key":        tftypes.String,
		"is_active":  tftypes.Bool,
		"is_public":  tftypes.Bool,
		"created_at": tftypes.String,
	}}, nil)
}

// boolPlanRequest builds a request from two optional raw is_public values. A
// absent value means the whole object is null.
func boolPlanRequest(t *testing.T, state, plan *tftypes.Value) planmodifier.BoolRequest {
	t.Helper()
	s := apiKeySchemaForPlanTest(t)

	req := planmodifier.BoolRequest{
		State:      tfsdk.State{Schema: s, Raw: nullWholeObject()},
		Plan:       tfsdk.Plan{Schema: s, Raw: nullWholeObject()},
		StateValue: types.BoolNull(),
		PlanValue:  types.BoolNull(),
	}
	if state != nil {
		req.State = tfsdk.State{Schema: s, Raw: rawAPIKey(*state)}
		var v types.Bool
		if diags := req.State.GetAttribute(context.Background(), path.Root("is_public"), &v); diags.HasError() {
			t.Fatalf("reading state is_public: %v", diags)
		}
		req.StateValue = v
	}
	if plan != nil {
		req.Plan = tfsdk.Plan{Schema: s, Raw: rawAPIKey(*plan)}
		var v types.Bool
		if diags := req.Plan.GetAttribute(context.Background(), path.Root("is_public"), &v); diags.HasError() {
			t.Fatalf("reading plan is_public: %v", diags)
		}
		req.PlanValue = v
	}
	return req
}

// TestVisibilityFixingReplacementIsNarrow is the guard the resource-level tests
// cannot provide: the modifier must ask for a replacement ONLY when the
// visibility value itself changed.
//
// The stakes are asymmetric and both directions are damaging:
//
//   - too broad: every unrelated edit — a rename, a description, an is_active
//     toggle — destroys and recreates the object. For an API key that mints a
//     new secret and invalidates the old one, so editing a label silently breaks
//     whatever was using the credential. For an endpoint it changes the URL.
//   - too narrow: a real visibility change is attempted in place, the API drops
//     the ownership field, and the apply fails with "inconsistent result after
//     apply" after the object has already been modified.
func TestVisibilityFixingReplacementIsNarrow(t *testing.T) {
	bv := func(b bool) *tftypes.Value {
		v := tftypes.NewValue(tftypes.Bool, b)
		return &v
	}
	nullVal := func() *tftypes.Value {
		v := tftypes.NewValue(tftypes.Bool, nil)
		return &v
	}
	unknownVal := func() *tftypes.Value {
		v := tftypes.NewValue(tftypes.Bool, tftypes.UnknownValue)
		return &v
	}

	for _, tc := range []struct {
		name        string
		state       *tftypes.Value // nil means "no state: a create"
		plan        *tftypes.Value // nil means "no plan: a destroy"
		wantReplace bool
	}{
		{
			name:        "create has nothing to replace",
			state:       nil,
			plan:        bv(false),
			wantReplace: false,
		},
		{
			name:        "destroy has nothing to replace",
			state:       bv(false),
			plan:        nil,
			wantReplace: false,
		},
		{
			// The counterweight, and the reason this test exists: an unchanged
			// value must leave the resource alone. Removing the Equal guard in
			// the modifier fails exactly here.
			name:        "unchanged false does not replace",
			state:       bv(false),
			plan:        bv(false),
			wantReplace: false,
		},
		{
			name:        "unchanged true does not replace",
			state:       bv(true),
			plan:        bv(true),
			wantReplace: false,
		},
		{
			// Optional+Computed: unset in state and unset in the plan is a
			// no-op, not a change to public.
			name:        "null to null does not replace",
			state:       nullVal(),
			plan:        nullVal(),
			wantReplace: false,
		},
		{
			name:        "private to public replaces",
			state:       bv(false),
			plan:        bv(true),
			wantReplace: true,
		},
		{
			name:        "public to private replaces",
			state:       bv(true),
			plan:        bv(false),
			wantReplace: true,
		},
		{
			// An unknown plan value cannot be compared: it is the framework's
			// "cannot yet decide", so the next plan resolves it. Forcing a
			// replacement here would destroy the object on a plan that may never
			// be applied.
			name:        "unknown plan value does not replace",
			state:       bv(false),
			plan:        unknownVal(),
			wantReplace: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := boolPlanRequest(t, tc.state, tc.plan)
			resp := &planmodifier.BoolResponse{PlanValue: req.PlanValue}

			visibilityFixingReplacement{}.PlanModifyBool(context.Background(), req, resp)

			if resp.RequiresReplace != tc.wantReplace {
				t.Errorf("RequiresReplace = %v, want %v (state=%v plan=%v)",
					resp.RequiresReplace, tc.wantReplace, req.StateValue, req.PlanValue)
			}
			// The modifier must never invent a value. On a replacement the
			// planned value has to survive so the new object is created with the
			// visibility that was asked for; changing it would silently alter
			// what the user configured.
			if !resp.PlanValue.Equal(req.PlanValue) {
				t.Errorf("PlanValue = %v, want it left as %v", resp.PlanValue, req.PlanValue)
			}
		})
	}
}

// apiKeySchemaForPlanTest returns the real metamcp_api_key schema, which carries
// the optional+computed is_public attribute the modifier is attached to in every
// resource. Using the genuine schema rather than a hand-built stub means this
// test cannot drift from how the modifier is actually configured.
func apiKeySchemaForPlanTest(t *testing.T) schema.Schema {
	t.Helper()
	r := &apiKeyResource{}
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	return resp.Schema
}
