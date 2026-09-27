resource "metamcp_api_key" "ci" {
  name      = "ci-pipeline"
  is_active = true
}

# The key value is returned only when the key is created or listed, and is
# stored in Terraform state: treat state as a secret store.
output "ci_key" {
  value     = metamcp_api_key.ci.key
  sensitive = true
}
