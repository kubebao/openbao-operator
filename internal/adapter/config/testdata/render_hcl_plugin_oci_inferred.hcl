ui               = true
cluster_name     = "modern-plugin"
api_addr         = "https://$${HOSTNAME}.modern-plugin.default.svc:8200"
cluster_addr     = "https://$${HOSTNAME}.modern-plugin.default.svc:8201"
plugin_directory = "/openbao/plugins"
listener "tcp" {
  address                  = "[::]:8200"
  cluster_address          = "[::]:8201"
  tls_disable              = 0
  max_request_duration     = "90s"
  tls_cert_file            = "/etc/bao/tls/tls.crt"
  tls_key_file             = "/etc/bao/tls/tls.key"
  tls_client_ca_file       = "/etc/bao/tls/ca.crt"
  tls_auto_reload          = true
  tls_auto_reload_interval = "10s"
}
seal "static" {
  current_key    = "file:///etc/bao/unseal/key"
  current_key_id = "operator-generated-v1"
}
storage "raft" {
  path    = "/bao/data"
  node_id = "$${HOSTNAME}"
  retry_join {
    auto_join               = "provider=k8s namespace=default label_selector=\"openbao.org/cluster=modern-plugin\""
    leader_tls_servername   = "openbao-cluster-modern-plugin.local"
    leader_ca_cert_file     = "/etc/bao/tls/ca.crt"
    leader_client_cert_file = "/etc/bao/tls/tls.crt"
    leader_client_key_file  = "/etc/bao/tls/tls.key"
  }
}
service_registration "kubernetes" {
}
plugin "kms" "example" {
  image = "registry.example.test/kms:v1.0.0@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
}
