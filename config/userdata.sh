#!/bin/bash

case $(uname -m) in
	aarch64)	ARCH="arm64";;
	x86_64)		ARCH="amd64";;
	*)		ARCH="$(uname -m)";;
esac

# Download and configure CNI
cni_bin_dir="/opt/cni/bin"

CNI_VERSION=v1.2.0 &&\
mkdir -p ${cni_bin_dir} &&\
curl -fsSL https://github.com/containernetworking/plugins/releases/download/${CNI_VERSION}/cni-plugins-linux-${ARCH}-${CNI_VERSION}.tgz \
    | tar xfz - -C ${cni_bin_dir}

# install a CNI
sudo mkdir -p /etc/cni/net.d/
cat << __ECNI__ | sudo tee /etc/cni/net.d/10-testcni.conflist
{
  "cniVersion": "0.3.1",
  "name": "testcni",
  "plugins": [
    {
      "name": "testnet",
      "type": "bridge",
      "bridge": "cni0",
      "isGateway": true,
      "ipMasq": true,
      "ipam": {
        "type": "host-local",
        "subnet": "10.22.0.0/16",
        "routes": [
          {
            "dst": "0.0.0.0/0"
          }
        ]
      }
    }
  ]
}
__ECNI__

# one of the CRI tests needs an extra "test-handler" so add that at the end
cat <<EOF > /etc/containerd/config.toml
version = 2
root = "/var/lib/containerd"
state = "/run/containerd"
# Users can use the following import directory to add additional
# configuration to containerd. The imports do not behave exactly like overrides.
# see: https://github.com/containerd/containerd/blob/main/docs/man/containerd-config.toml.5.md#format
imports = ["/etc/containerd/config.d/*.toml"]
[grpc]
address = "/run/containerd/containerd.sock"
[plugins."io.containerd.grpc.v1.cri".containerd]
default_runtime_name = "runc"
discard_unpacked_layers = true
[plugins."io.containerd.grpc.v1.cri"]
sandbox_image = "registry.k8s.io/pause:3.8"
[plugins."io.containerd.grpc.v1.cri".registry]
config_path = "/etc/containerd/certs.d:/etc/docker/certs.d"
[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc]
runtime_type = "io.containerd.runc.v2"
[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc.options]
SystemdCgroup = true
[plugins."io.containerd.grpc.v1.cri".cni]
bin_dir = "/opt/cni/bin"
conf_dir = "/etc/cni/net.d"
# Setup a runtime with the magic name ("test-handler") used for Kubernetes
# runtime class tests ...
[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.test-handler]
  runtime_type = "io.containerd.runc.v2"
EOF

systemctl restart containerd

# hack systemd-run so it ignores the "-p StandardError=file:///some/file.log" option that isn't supported
# by systemd
sudo mv /usr/bin/systemd-run /usr/bin/systemd-run.real
cat << __ESYSD__ > /usr/bin/systemd-run
#!/usr/bin/env python3

import sys
import subprocess


actual_args = ["systemd-run.real"]
for arg in sys.argv[1:]:
 if arg.startswith('-E'):
  actual_args.append(arg.replace("-E","--setenv"))
 elif arg.startswith('StandardError'):
  # remove the -p
  actual_args.pop()
 else:
  actual_args.append(arg)

subprocess.run(actual_args)
__ESYSD__
chmod a+x /usr/bin/systemd-run
