storage "raft" {
 path = "/tmp/bao"
 retry_join { auto_joinn = "provider=k8s" }
}
