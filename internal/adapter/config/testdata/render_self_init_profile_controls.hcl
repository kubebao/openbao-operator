initialize "conditional-health" {
  request "conditional-health-request" {
    operation     = "read"
    path          = "sys/health"
    allow_failure = true
    headers = {
      X-OpenBao-Trace = ["trace-1", "trace-2"]
    }
    when = {
      eval_source = "cel"
      eval_type   = "bool"
      expression  = "true"
    }
  }
}
initialize "static-skip" {
  request "static-skip-request" {
    operation = "read"
    path      = "sys/seal-status"
    when      = false
  }
}
initialize "configure-autopilot" {
  request "configure-autopilot-request" {
    operation = "update"
    path      = "sys/storage/raft/autopilot/configuration"
    data {
      cleanup_dead_servers               = true
      dead_server_last_contact_threshold = "24h"
      min_quorum                         = "3"
      server_stabilization_time          = "10s"
    }
  }
}
