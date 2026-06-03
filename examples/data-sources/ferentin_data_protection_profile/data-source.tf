# Singular — look one profile up by name (plan-time validation that it exists).
data "ferentin_data_protection_profile" "us_pii" {
  name = "US_PII"
}

# Reference it from a policy instead of hardcoding the string.
resource "ferentin_data_protection_policy" "example" {
  name             = "pii-only"
  enabled_profiles = [data.ferentin_data_protection_profile.us_pii.name]
}
