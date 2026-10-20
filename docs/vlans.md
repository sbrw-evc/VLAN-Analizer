# VLAN interfaces on trunk ports

Administrators create 802.1Q VLAN subinterfaces on a port connected to a switch trunk under **Host network → VLAN
interfaces**. Each interface is written to the host's own network configuration, so it survives reboots and stays
visible to the host's tools. Interfaces with IPv4 join the availability matrix on the next cycle.

## How it works

The application container stays unprivileged (`NET_RAW` only). Changes go through the `netagent` service of
`compose.yaml`:

| | `app` | `netagent` |
| --- | --- | --- |
| Privileges | `cap_drop: ALL`, `NET_RAW` | `privileged: true`, `pid: host` |
| Network | host | none |
| Interface | HTTP on port 8000 | Unix socket `/run/vlan-agent/agent.sock` (mode 0600) in a volume shared only with `app` |

The agent enters the mount and network namespaces of the host's PID 1 (`nsenter`) and runs the host's own tools, so
the host decides how a VLAN is configured. It accepts only validated specifications (parent, VLAN ID, addressing,
MTU, metric) and builds every command from fixed templates. Nothing reaches a shell. Only administrators can call
`/api/network/vlans`, and state-changing calls must come from the application origin. To disable VLAN management,
remove the `netagent` service; the page then explains that the agent is not running.

## Supported distributions and backends

The agent reads `/etc/os-release` and chooses the persistence the host actually uses:

| Host | Backend | What is written | Applied with |
| --- | --- | --- | --- |
| Ubuntu (netplan with systemd-networkd or NetworkManager) | `netplan` | `/etc/netplan/90-vlan-analizer.yaml` (mode 0600, every managed VLAN) | `netplan generate`, `netplan apply` |
| RHEL, CentOS 7+/Stream, Rocky, AlmaLinux, Oracle Linux; Debian/Ubuntu desktops with NetworkManager | `networkmanager` | NetworkManager profile `vlan-analizer-<name>` | `nmcli connection add/up/down/delete` |
| Debian with ifupdown | `ifupdown` | `/etc/network/interfaces.d/vlan-analizer-<name>`; a `source` line is appended to `/etc/network/interfaces` if it does not read `interfaces.d` | `ifup`, `ifdown` |
| CentOS/RHEL 7 with network-scripts and no NetworkManager | `network-scripts` | `/etc/sysconfig/network-scripts/ifcfg-<name>` (`NM_CONTROLLED=no`) | `ifup`, `ifdown` |
| Anything else | `runtime` | nothing on the host; the agent re-creates the interfaces when it starts | `ip link`, `ip address`, `ip route` |

The list of managed interfaces is kept on the host in `/etc/vlan-analizer/vlans.json`, next to the configuration it
describes, so `docker compose down -v` does not orphan host configuration. Interfaces that the application did not
create are shown on their trunk but cannot be changed or deleted here. On SELinux hosts, written files get their
default label with `restorecon`.

## Naming, addressing and routes

- **Names** are `<parent>.<vid>` (for example `eno2.20`) or `vlan<vid>`; when `<parent>.<vid>` exceeds the kernel's
  15 characters, `vlan<vid>` is used. These are the forms ifupdown and network-scripts understand.
- **Batches**: one request creates up to 64 VLANs from a list such as `20, 30-35`. Each one is created separately and
  failures are reported per VLAN.
- **Addressing**: DHCP, static IPv4 (CIDR plus an optional gateway, one VLAN at a time) or no address. Interfaces
  without IPv4 are inventory only.
- **Routes**: probes bind to their interface, so each VLAN needs its own route to the destinations. DHCP routes and
  the static gateway are installed with metric `2000 + VLAN ID` (configurable), above the host's primary route, so
  the host keeps its default path while probes bound to the VLAN still find theirs.
- **Reverse-path filtering**: replies to probes arrive on the VLAN while the host's best route back may use the
  primary interface; strict `rp_filter` (the RHEL default) would drop them. Managed VLANs get `rp_filter = 2` (loose)
  at runtime and in `/etc/sysctl.d/90-vlan-analizer.conf`.
- **DNS servers** entered for a VLAN become its per-interface resolver override in the monitoring settings.
- **Monitoring**: with an automatic interface selection, new VLANs with IPv4 are probed automatically. Turning
  "Probe websites through these interfaces" off pins the current selection so that the new VLANs are left out.

## Changing and deleting

Editing addressing, MTU or the metric removes and re-creates the interface through the same backend, so probes
through it pause for a few seconds. If re-creation fails, the previous configuration is restored. Bringing an
interface down uses `nmcli connection down` or `ifdown` where available, otherwise `ip link set down` (until the next
reboot). Deleting removes the host configuration, the kernel device, the sysctl entry and its monitoring overrides.

## Requirements and limits

- The kernel needs the `8021q` module (standard on Debian, Ubuntu, CentOS and RHEL). Without it the agent answers
  "the host kernel has no 802.1Q VLAN support; load the 8021q module".
- Parents can be physical interfaces, bonds, teams and bridges; Docker, veth, libvirt and CNI devices are excluded.
- The switch port must be a trunk that carries the VLANs as tagged frames. The application does not configure
  switches.
- IPv4 only: managed VLANs get no IPv6 configuration. Stacked VLANs (802.1ad / QinQ) are not supported.
- On netplan hosts, a parent that the host's netplan files do not define is declared as a trunk port without
  addresses in `90-vlan-analizer.yaml` (`dhcp4: false`, `optional: true`).
- `NETAGENT_HOST_PID` selects another host process whose namespaces the agent enters (default 1). It exists for
  nested test environments; production hosts keep the default.
