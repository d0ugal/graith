# Terraform snippet for the GitHub Actions secrets required by the stable
# Darwin release job. Copy this into the Terraform configuration that manages
# the d0ugal/graith repository.
#
# These are repository-level Actions secrets because .github/workflows/goreleaser.yml
# reads them as ${{ secrets.NAME }} and does not bind an environment.
#
# The integrations/github provider stores plaintext_value in Terraform state.
# Keep that state encrypted and tightly access-controlled, or adapt this snippet
# to pre-encrypted values if your state policy forbids plaintext secrets.
#
# The consuming root module should already configure the integrations/github
# provider and its owner. Add or merge a required_providers block there if the
# provider is not already declared.

variable "graith_repository" {
  description = "Repository name managed by the configured GitHub provider owner."
  type        = string
  default     = "graith"
}

variable "graith_stable_release_macos_secrets" {
  description = <<EOT
Complete macOS signing and notarization credential set for the stable release.

Expected value formats:
- macos_signing_certificate: base64-encoded PKCS#12 .p12 Developer ID Application identity.
- macos_signing_certificate_password: password chosen when exporting the .p12 identity.
- macos_signing_identity: exact codesign identity, for example "Developer ID Application: Dougal Matthews (JYV24W6X2A)".
- macos_signing_team_id: Apple Developer Team ID, for example "JYV24W6X2A".
- macos_signing_requirement: exact designated requirement reported by codesign for Graith.app.
- apple_notary_private_key: full App Store Connect API .p8 private key contents, including PEM header/footer.
- apple_notary_key_id: App Store Connect API key ID.
- apple_notary_issuer_id: App Store Connect API issuer ID.
EOT
  type = object({
    macos_signing_certificate          = string
    macos_signing_certificate_password = string
    macos_signing_identity             = string
    macos_signing_team_id              = string
    macos_signing_requirement          = string
    apple_notary_private_key           = string
    apple_notary_key_id                = string
    apple_notary_issuer_id             = string
  })
  sensitive = true

  validation {
    condition = alltrue([
      length(trimspace(var.graith_stable_release_macos_secrets.macos_signing_certificate)) > 0,
      length(trimspace(var.graith_stable_release_macos_secrets.macos_signing_certificate_password)) > 0,
      length(trimspace(var.graith_stable_release_macos_secrets.macos_signing_identity)) > 0,
      length(trimspace(var.graith_stable_release_macos_secrets.macos_signing_team_id)) > 0,
      length(trimspace(var.graith_stable_release_macos_secrets.macos_signing_requirement)) > 0,
      length(trimspace(var.graith_stable_release_macos_secrets.apple_notary_private_key)) > 0,
      length(trimspace(var.graith_stable_release_macos_secrets.apple_notary_key_id)) > 0,
      length(trimspace(var.graith_stable_release_macos_secrets.apple_notary_issuer_id)) > 0,
    ])
    error_message = "All stable release macOS signing and notarization secret values must be non-empty."
  }
}

locals {
  graith_stable_release_macos_secret_values = {
    MACOS_SIGNING_CERTIFICATE          = var.graith_stable_release_macos_secrets.macos_signing_certificate
    MACOS_SIGNING_CERTIFICATE_PASSWORD = var.graith_stable_release_macos_secrets.macos_signing_certificate_password
    MACOS_SIGNING_IDENTITY             = var.graith_stable_release_macos_secrets.macos_signing_identity
    MACOS_SIGNING_TEAM_ID              = var.graith_stable_release_macos_secrets.macos_signing_team_id
    MACOS_SIGNING_REQUIREMENT          = var.graith_stable_release_macos_secrets.macos_signing_requirement
    APPLE_NOTARY_PRIVATE_KEY           = var.graith_stable_release_macos_secrets.apple_notary_private_key
    APPLE_NOTARY_KEY_ID                = var.graith_stable_release_macos_secrets.apple_notary_key_id
    APPLE_NOTARY_ISSUER_ID             = var.graith_stable_release_macos_secrets.apple_notary_issuer_id
  }
}

resource "github_actions_secret" "graith_stable_release_macos" {
  for_each = local.graith_stable_release_macos_secret_values

  repository      = var.graith_repository
  secret_name     = each.key
  plaintext_value = each.value
}
