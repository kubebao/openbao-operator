ui           = true
cluster_name = "azure-seal-owi"
api_addr     = "https://$${HOSTNAME}.azure-seal-owi.default.svc:8200"
cluster_addr = "https://$${HOSTNAME}.azure-seal-owi.default.svc:8201"
listener "tcp" {
  address              = "[::]:8200"
  cluster_address      = "[::]:8201"
  tls_disable          = 0
  max_request_duration = "90s"
  tls_cert_file        = "/etc/bao/tls/tls.crt"
  tls_key_file         = "/etc/bao/tls/tls.key"
  tls_client_ca_file   = "/etc/bao/tls/ca.crt"
}
seal "azurekeyvault" {
  vault_name  = "my-vault"
  key_name    = "my-key"
  tenant_id   = "tenant-123"
  auth_method = "workload_identity"
}
storage "raft" {
  path    = "/bao/data"
  node_id = "$${HOSTNAME}"
  retry_join {
    auto_join               = "provider=k8s namespace=default label_selector=\"openbao.org/cluster=azure-seal-owi\""
    leader_tls_servername   = "openbao-cluster-azure-seal-owi.local"
    leader_ca_cert_file     = "/etc/bao/tls/ca.crt"
    leader_client_cert_file = "/etc/bao/tls/tls.crt"
    leader_client_key_file  = "/etc/bao/tls/tls.key"
  }
}
service_registration "kubernetes" {
}
