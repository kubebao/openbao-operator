ui           = true
cluster_name = "telemetry-options"
api_addr     = "https://$${HOSTNAME}.telemetry-options.default.svc:8200"
cluster_addr = "https://$${HOSTNAME}.telemetry-options.default.svc:8201"
listener "tcp" {
  address              = "[::]:8200"
  cluster_address      = "[::]:8201"
  tls_disable          = 0
  max_request_duration = "90s"
  tls_cert_file        = "/etc/bao/tls/tls.crt"
  tls_key_file         = "/etc/bao/tls/tls.key"
  tls_client_ca_file   = "/etc/bao/tls/ca.crt"
}
seal "static" {
  current_key    = "file:///etc/bao/unseal/key"
  current_key_id = "operator-generated-v1"
}
storage "raft" {
  path    = "/bao/data"
  node_id = "$${HOSTNAME}"
  retry_join {
    auto_join               = "provider=k8s namespace=default label_selector=\"openbao.org/cluster=telemetry-options\""
    leader_tls_servername   = "openbao-cluster-telemetry-options.local"
    leader_ca_cert_file     = "/etc/bao/tls/ca.crt"
    leader_client_cert_file = "/etc/bao/tls/tls.crt"
    leader_client_key_file  = "/etc/bao/tls/tls.key"
  }
}
service_registration "kubernetes" {
}
telemetry {
  usage_gauge_period                     = "10m"
  maximum_gauge_cardinality              = 2000
  disable_hostname                       = true
  enable_hostname_label                  = true
  metrics_prefix                         = "compat"
  lease_metrics_epsilon                  = "1h"
  prometheus_retention_time              = "30s"
  statsite_address                       = "127.0.0.1:8125"
  statsd_address                         = "127.0.0.1:8126"
  dogstatsd_addr                         = "127.0.0.1:8127"
  dogstatsd_tags                         = ["env:test"]
  circonus_api_token                     = "test-token"
  circonus_api_app                       = "test-app"
  circonus_api_url                       = "https://api.example.test"
  circonus_submission_interval           = "10s"
  circonus_check_id                      = "1"
  circonus_check_force_metric_activation = "true"
  circonus_check_instance_id             = "compat"
  circonus_check_search_tag              = "env:test"
  circonus_check_display_name            = "compat"
  circonus_check_tags                    = "env:test"
  circonus_broker_id                     = "1"
  circonus_broker_select_tag             = "env:test"
  stackdriver_project_id                 = "test-project"
  stackdriver_location                   = "test-region"
  stackdriver_namespace                  = "compat"
  stackdriver_debug_logs                 = true
}
