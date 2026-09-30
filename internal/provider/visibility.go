package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
)

// visibilityFixingReplacement forces a replacement when is_public changes on a
// resource whose ownership the MetaMCP API cannot update after creation.
//
// Some update procedures simply do not accept the ownership field — only their
// create path does — so a changed `user_id` is dropped on the floor. Left as an
// in-place update, Terraform sends the new ownership, the API silently keeps the
// old one, and the read-back then disagrees with the plan:
//
//	Provider produced inconsistent result after apply
//	.is_public: was cty.True, but now cty.False
//
// which surfaces *after* the resource has already been changed. Failing loudly at
// plan time is the alternative, but it would make the whole resource
// unmanageable for anyone who only wanted to change an unrelated attribute, so
// replacing is the lesser evil: it is what the API forces, made explicit.
//
// Every resource using this should pair it with a refusal in its own Update, in
// case the modifier is ever bypassed — a silent divergence is the worst outcome.
//
// The modifier is deliberately narrow, and that is load-bearing: it only fires
// when the visibility value itself differs. A broader rule (or a method that
// always sets RequiresReplace) would destroy and recreate the resource — and its
// URL, or its API key — on every unrelated edit. Each resource's test suite pins
// both directions: a visibility change destroys and creates, an ordinary edit
// updates in place.
type visibilityFixingReplacement struct{}

func (visibilityFixingReplacement) Description(_ context.Context) string {
	return "changing visibility requires replacing the object, because the " +
		"MetaMCP API cannot update its ownership"
}

func (m visibilityFixingReplacement) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (visibilityFixingReplacement) PlanModifyBool(_ context.Context, req planmodifier.BoolRequest, resp *planmodifier.BoolResponse) {
	// A create or destroy has nothing to replace.
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	// An unknown value cannot be compared. It is also the case the framework
	// treats as "cannot yet decide", so the next plan resolves it.
	if req.PlanValue.IsUnknown() || req.StateValue.IsUnknown() {
		return
	}
	if req.PlanValue.Equal(req.StateValue) {
		return
	}
	resp.RequiresReplace = true
	// Keep the planned value and let the framework carry it to the replacement,
	// so the new object is created with the visibility that was asked for.
	resp.PlanValue = req.PlanValue
}
