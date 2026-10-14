ui           = true
cluster_name = "acme-ca"
api_addr     = "https://$${HOSTNAME}.acme-ca.default.svc:8200"
cluster_addr = "https://$${HOSTNAME}.acme-ca.default.svc:8201"
listener "tcp" {
  address                         = "[::]:8200"
  cluster_address                 = "[::]:8201"
  tls_disable                     = 0
  max_request_duration            = "90s"
  tls_acme_ca_directory           = "https://acme.example.test/directory"
  tls_acme_domains                = ["acme-ca-acme.default.svc"]
  tls_acme_cache_path             = "/bao/data/certmagic"
  tls_acme_disable_http_challenge = true
  tls_acme_ca_root                = "/etc/acme/ca.crt"
}
seal "static" {
  current_key    = "file:///etc/bao/unseal/key"
  current_key_id = "operator-generated-v1"
}
storage "raft" {
  path    = "/bao/data"
  node_id = "$${HOSTNAME}"
  retry_join {
    auto_join             = "provider=k8s namespace=default label_selector=\"openbao.org/cluster=acme-ca\""
    leader_tls_servername = "acme-ca-acme.default.svc"
    leader_ca_cert_file   = "/etc/acme/pki-ca.crt"
  }
}
service_registration "kubernetes" {
}
