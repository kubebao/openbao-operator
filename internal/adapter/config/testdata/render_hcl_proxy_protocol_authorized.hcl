ui           = true
cluster_name = "proxy-protocol"
api_addr     = "https://$${HOSTNAME}.proxy-protocol.default.svc:8200"
cluster_addr = "https://$${HOSTNAME}.proxy-protocol.default.svc:8201"
listener "tcp" {
  address                         = "[::]:8200"
  cluster_address                 = "[::]:8201"
  tls_disable                     = 0
  max_request_duration            = "90s"
  proxy_protocol_behavior         = "allow_authorized"
  proxy_protocol_authorized_addrs = ["192.0.2.10", "198.51.100.0/24", "2001:db8::10", "2001:db8:1::/64"]
  tls_cert_file                   = "/etc/bao/tls/tls.crt"
  tls_key_file                    = "/etc/bao/tls/tls.key"
  tls_client_ca_file              = "/etc/bao/tls/ca.crt"
  tls_auto_reload                 = true
  tls_auto_reload_interval        = "10s"
  telemetry {
    disallow_metrics = true
  }
}
listener "tcp" {
  address                  = "[::]:8202"
  tls_disable              = 0
  max_request_duration     = "90s"
  tls_cert_file            = "/etc/bao/tls/tls.crt"
  tls_key_file             = "/etc/bao/tls/tls.key"
  tls_client_ca_file       = "/etc/bao/tls/ca.crt"
  tls_auto_reload          = true
  tls_auto_reload_interval = "10s"
  telemetry {
    unauthenticated_metrics_access = true
    metrics_only                   = true
  }
}
seal "static" {
  current_key    = "file:///etc/bao/unseal/key"
  current_key_id = "operator-generated-v1"
}
storage "raft" {
  path    = "/bao/data"
  node_id = "$${HOSTNAME}"
  retry_join {
    auto_join               = "provider=k8s namespace=default label_selector=\"openbao.org/cluster=proxy-protocol\""
    leader_tls_servername   = "openbao-cluster-proxy-protocol.local"
    leader_ca_cert_file     = "/etc/bao/tls/ca.crt"
    leader_client_cert_file = "/etc/bao/tls/tls.crt"
    leader_client_key_file  = "/etc/bao/tls/tls.key"
  }
}
service_registration "kubernetes" {
}
telemetry {
  disable_hostname          = true
  prometheus_retention_time = "30s"
}
