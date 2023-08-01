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
sed -i'' 's#SystemdCgroup = .*#SystemdCgroup = true#' /etc/containerd/config.toml

systemctl restart containerd
