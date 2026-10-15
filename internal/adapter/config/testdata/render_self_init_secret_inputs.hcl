initialize "human-oidc-config" {
  request "human-oidc-config-request" {
    operation = "update"
    path      = "auth/human/config"
    data {
      default_role   = "platform-admin"
      oidc_client_id = "application-id"
      oidc_client_secret = {
        eval_source = "file"
        eval_type   = "string"
        path        = "/etc/bao/self-init-inputs/1cf5c30e149fab16de1ebecf5a19f62094ae8082008cd0f80cabf316a691e07b"
      }
    }
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
