path "secret/data/vlan-analizer/app" {
  capabilities = ["read"]
}
path "secret/data/vlan-analizer/users/*" {
  capabilities = ["create", "read", "update"]
}
