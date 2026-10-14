ui               = true
cluster_name     = "kms-plugin-seal"
api_addr         = "https://$${HOSTNAME}.kms-plugin-seal.default.svc:8200"
cluster_addr     = "https://$${HOSTNAME}.kms-plugin-seal.default.svc:8201"
plugin_directory = "/openbao/plugins"
listener "tcp" {
  address              = "[::]:8200"
  cluster_address      = "[::]:8201"
  tls_disable          = 0
  max_request_duration = "90s"
  tls_cert_file        = "/etc/bao/tls/tls.crt"
  tls_key_file         = "/etc/bao/tls/tls.key"
  tls_client_ca_file   = "/etc/bao/tls/ca.crt"
}
seal "corp-kms" {
  ca_file    = "/etc/bao/seal-creds/ca.pem"
  cluster_id = "prod-eu1"
  endpoint   = "https://kms.internal.example"
  mode       = "envelope"
  node_id    = "openbao.openbao"
}
storage "raft" {
  path    = "/bao/data"
  node_id = "$${HOSTNAME}"
  retry_join {
    auto_join               = "provider=k8s namespace=default label_selector=\"openbao.org/cluster=kms-plugin-seal\""
    leader_tls_servername   = "openbao-cluster-kms-plugin-seal.local"
    leader_ca_cert_file     = "/etc/bao/tls/ca.crt"
    leader_client_cert_file = "/etc/bao/tls/tls.crt"
    leader_client_key_file  = "/etc/bao/tls/tls.key"
  }
}
service_registration "kubernetes" {
}
plugin "kms" "corp-kms" {
  command     = "openbao-kms-corp"
  version     = "0.0.0-dev"
  binary_name = "openbao-kms-corp"
  sha256sum   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
}
