ui           = true
cluster_name = "seal-options"
api_addr     = "https://$${HOSTNAME}.seal-options.default.svc:8200"
cluster_addr = "https://$${HOSTNAME}.seal-options.default.svc:8201"
listener "tcp" {
  address              = "[::]:8200"
  cluster_address      = "[::]:8201"
  tls_disable          = 0
  max_request_duration = "90s"
  tls_cert_file        = "/etc/bao/tls/tls.crt"
  tls_key_file         = "/etc/bao/tls/tls.key"
  tls_client_ca_file   = "/etc/bao/tls/ca.crt"
}
seal "awskms" {
  region        = "eu-west-1"
  kms_key_id    = "test-key"
  access_key    = "test-access"
  secret_key    = "test-secret"
  session_token = "test-session"
}
storage "raft" {
  path    = "/bao/data"
  node_id = "$${HOSTNAME}"
  retry_join {
    auto_join               = "provider=k8s namespace=default label_selector=\"openbao.org/cluster=seal-options\""
    leader_tls_servername   = "openbao-cluster-seal-options.local"
    leader_ca_cert_file     = "/etc/bao/tls/ca.crt"
    leader_client_cert_file = "/etc/bao/tls/tls.crt"
    leader_client_key_file  = "/etc/bao/tls/tls.key"
  }
}
service_registration "kubernetes" {
}
