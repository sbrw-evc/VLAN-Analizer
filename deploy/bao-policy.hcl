# For an existing AppRole supplied to the first-run wizard.
# Replace secret if you use another KV v2 mount.
path "secret/data/vlan-analizer/app" {
  capabilities = ["create", "read"]
}
path "secret/data/vlan-analizer/users/*" {
  capabilities = ["create", "read", "update"]
}
path "secret/data/vlan-analizer/connections/*" {
  capabilities = ["create", "read", "update"]
}
