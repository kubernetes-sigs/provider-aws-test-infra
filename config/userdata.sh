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

# use nodeadm to generate containerd's config.toml
cat <<EOF > /tmp/nodeadm.yaml
apiVersion: node.eks.aws/v1alpha1
kind: NodeConfig
spec:
  cluster:
    name: my-cluster
    apiServerEndpoint: https://example.com
    certificateAuthority: Y2VydGlmaWNhdGVBdXRob3JpdHk=
    cidr: 10.100.0.0/16
  containerd:
    config: |
      [plugins."io.containerd.grpc.v1.cri"]
      sandbox_image = "registry.k8s.io/pause:3.8"
EOF

/usr/bin/nodeadm init --skip run --daemon containerd --config-source file:///tmp/nodeadm.yaml

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
