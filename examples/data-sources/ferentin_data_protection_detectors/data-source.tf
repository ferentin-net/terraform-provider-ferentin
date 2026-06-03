# Plural — lists every built-in detector.
data "ferentin_data_protection_detectors" "all" {}

output "fpe_safe_detectors" {
  value = [for d in data.ferentin_data_protection_detectors.all.detectors : d.id if d.fpe_safe]
}
