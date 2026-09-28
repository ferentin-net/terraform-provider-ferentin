variable "otlp_sink_id" {
  type        = string
  description = "ID of the ferentin_otel_sink to send to. In a real config this is `ferentin_otel_sink.<name>.sink_id`."
}

resource "ferentin_otel_policy" "default_traces" {
  name        = "default-traces"
  description = "Send all traces to the primary OTLP sink"
  priority    = 100
  enabled     = true

  sink_ids = [var.otlp_sink_id]
  signals  = ["traces", "metrics"]
}
