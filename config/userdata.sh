#!/bin/bash

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

# use the EKS AMI version of the containerd config
cp /etc/eks/containerd/containerd-config.toml /etc/containerd/config.toml
# rewrite the pause image url
sed -i'' 's#SANDBOX_IMAGE#registry.k8s.io/pause:3.8#' /etc/containerd/config.toml
# use cgroupfs as the containerd cgroup driver
sed -i'' 's#SystemdCgroup = .*#SystemdCgroup = false#' /etc/containerd/config.toml
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
 if arg.startswith('StandardError'):
  # remove the -p
  actual_args.pop()
 else:
  actual_args.append(arg)

subprocess.run(actual_args)
__ESYSD__
chmod a+x /usr/bin/systemd-run
