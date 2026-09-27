data "ferentin_otel_sink_provider" "otlp" {
  slug = "otlp"
}

resource "ferentin_otel_sink" "primary_otlp" {
  name      = "primary-otlp"
  endpoint  = "https://otel-collector.example.com:4318"
  sink_type = "OTLP_HTTP"

  provider_slug = data.ferentin_otel_sink_provider.otlp.slug
  protocol      = "HTTP"
  compression   = "gzip"
  auth_type     = "bearer"
  timeout       = "30s"
  enabled       = true

  headers = {
    "X-Tenant" = "acme"
  }

  tags = {
    env = "prod"
  }
}
