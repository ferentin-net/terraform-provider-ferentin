data "ferentin_otel_sink_provider" "otlp" {
  slug = "otlp"
}

output "otlp_name" {
  value = data.ferentin_otel_sink_provider.otlp.display_name
}
