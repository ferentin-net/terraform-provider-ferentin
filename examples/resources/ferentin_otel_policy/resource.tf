resource "ferentin_otel_policy" "default_traces" {
  name        = "default-traces"
  description = "Send all traces to the primary OTLP sink"
  priority    = 100
  enabled     = true

  sink_ids = [ferentin_otel_sink.primary_otlp.sink_id]
  signals  = ["traces", "metrics"]
}
