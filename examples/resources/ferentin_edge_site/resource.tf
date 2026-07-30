# Single edge site, minimal config.
resource "ferentin_edge_site" "us_east" {
  site_id   = "prod-us-east-1a"
  site_name = "US East 1A"

  # Free-form key/value tags for organizing and filtering sites. Not
  # interpreted by the platform — routing and bundling come from the typed
  # attributes. Dropping the block leaves the server-side value alone; set
  # `tags = {}` to clear.
  tags = {
    tier = "primary"
    team = "platform"
  }
}

# Multiple edge sites via for_each. Adding a new region is a one-line diff.
locals {
  edge_sites = {
    "prod-us-east-1a" = {
      site_name   = "US East 1A"
      description = "Primary US edge"
    }
    "prod-eu-west-1a" = {
      site_name   = "EU West 1A"
      description = "GDPR-bound traffic"
      time_zone   = "Europe/Dublin"
    }
    "prod-ap-south-1a" = {
      site_name          = "AP South 1A"
      description        = "India-resident traffic"
      max_edge_devices   = 10
      monitoring_enabled = true
    }
  }
}

resource "ferentin_edge_site" "regions" {
  for_each = local.edge_sites

  site_id            = each.key
  site_name          = each.value.site_name
  description        = each.value.description
  time_zone          = try(each.value.time_zone, null)
  max_edge_devices   = try(each.value.max_edge_devices, null)
  monitoring_enabled = try(each.value.monitoring_enabled, null)
}

output "us_east_synthetic_id" {
  description = "Server-generated UUID for the US East site, distinct from the slug."
  value       = ferentin_edge_site.us_east.synthetic_id
}
