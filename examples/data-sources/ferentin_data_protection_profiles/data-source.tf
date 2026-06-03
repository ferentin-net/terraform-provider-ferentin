# Plural — lists every built-in detector profile.
data "ferentin_data_protection_profiles" "all" {}

output "profile_names" {
  value = [for p in data.ferentin_data_protection_profiles.all.profiles : p.name]
}
