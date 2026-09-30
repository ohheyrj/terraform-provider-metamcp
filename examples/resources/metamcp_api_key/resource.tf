resource "metamcp_api_key" "ci" {
  name      = "ci-pipeline"
  is_active = true

  # Public: usable by every MetaMCP user, not just its owner. This is the
  # Public/Private toggle the MetaMCP web UI shows on the API keys page.
  #
  # It is set here at creation only — the API cannot change a key's visibility
  # afterwards, so the provider replaces the key instead, which issues a NEW
  # secret and invalidates this one. Omit the attribute to keep the key
  # private (the default).
  is_public = true
}

# The key value is returned only when the key is created or listed, and is
# stored in Terraform state: treat state as a secret store.
output "ci_key" {
  value     = metamcp_api_key.ci.key
  sensitive = true
}
