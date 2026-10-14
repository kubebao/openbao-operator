ui           = true
cluster_name = "raft-options"
api_addr     = "https://$${HOSTNAME}.raft-options.default.svc:8200"
cluster_addr = "https://$${HOSTNAME}.raft-options.default.svc:8201"
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
  path                    = "/bao/data"
  node_id                 = "$${HOSTNAME}"
  performance_multiplier  = 3
  retry_join_as_non_voter = true
  retry_join {
    auto_join               = "provider=k8s namespace=default label_selector=\"openbao.org/cluster=raft-options\""
    leader_tls_servername   = "openbao-cluster-raft-options.local"
    leader_ca_cert_file     = "/etc/bao/tls/ca.crt"
    leader_client_cert_file = "/etc/bao/tls/tls.crt"
    leader_client_key_file  = "/etc/bao/tls/tls.key"
  }
}
service_registration "kubernetes" {
}
