#!/bin/bash

set -o xtrace
set -xeuo pipefail

# one of the ci job tests needs git
yum install git -y

# Start with a clean slate
# Note that the `iptables -P FORWARD ACCEPT` piece is load bearing! https://github.com/search?q=repo%3Aawslabs%2Famazon-eks-ami+%22iptables+-P%22&type=code
iptables -F && iptables -X  && iptables -t nat -F  && iptables -t nat -X && iptables -t mangle -F  && iptables -t mangle -X  && iptables -P INPUT ACCEPT  && iptables -P FORWARD ACCEPT -w 5 && iptables -P OUTPUT ACCEPT -w 5

echo "Add rules for ip masquerade"
iptables -w -t nat -N IP-MASQ
iptables -w -t nat -A POSTROUTING -m comment --comment "ip-masq: ensure nat POSTROUTING directs all non-LOCAL destination traffic to our custom IP-MASQ chain" -m addrtype ! --dst-type LOCAL -j IP-MASQ
iptables -w -t nat -A IP-MASQ -d 169.254.0.0/16 -m comment --comment "ip-masq: local traffic is not subject to MASQUERADE" -j RETURN
iptables -w -t nat -A IP-MASQ -d 10.0.0.0/8 -m comment --comment "ip-masq: RFC 1918 reserved range is not subject to MASQUERADE" -j RETURN
iptables -w -t nat -A IP-MASQ -d 172.16.0.0/12 -m comment --comment "ip-masq: RFC 1918 reserved range is not subject to MASQUERADE" -j RETURN
iptables -w -t nat -A IP-MASQ -d 192.168.0.0/16 -m comment --comment "ip-masq: RFC 1918 reserved range is not subject to MASQUERADE" -j RETURN
iptables -w -t nat -A IP-MASQ -d 240.0.0.0/4 -m comment --comment "ip-masq: RFC 5735 reserved range is not subject to MASQUERADE" -j RETURN
iptables -w -t nat -A IP-MASQ -d 192.0.2.0/24 -m comment --comment "ip-masq: RFC 5737 reserved range is not subject to MASQUERADE" -j RETURN
iptables -w -t nat -A IP-MASQ -d 198.51.100.0/24 -m comment --comment "ip-masq: RFC 5737 reserved range is not subject to MASQUERADE" -j RETURN
iptables -w -t nat -A IP-MASQ -d 203.0.113.0/24 -m comment --comment "ip-masq: RFC 5737 reserved range is not subject to MASQUERADE" -j RETURN
iptables -w -t nat -A IP-MASQ -d 100.64.0.0/10 -m comment --comment "ip-masq: RFC 6598 reserved range is not subject to MASQUERADE" -j RETURN
iptables -w -t nat -A IP-MASQ -d 198.18.0.0/15 -m comment --comment "ip-masq: RFC 6815 reserved range is not subject to MASQUERADE" -j RETURN
iptables -w -t nat -A IP-MASQ -d 192.0.0.0/24 -m comment --comment "ip-masq: RFC 6890 reserved range is not subject to MASQUERADE" -j RETURN
iptables -w -t nat -A IP-MASQ -d 192.88.99.0/24 -m comment --comment "ip-masq: RFC 7526 reserved range is not subject to MASQUERADE" -j RETURN
iptables -w -t nat -A IP-MASQ -m comment --comment "ip-masq: outbound traffic is subject to MASQUERADE (must be last in chain)" -j MASQUERADE

if [ "$(uname -m)" = "arm64" ] || [ "$(uname -m)" = "aarch64" ]; then
  ARCH=arm64
else
  ARCH=amd64
fi

os=$( . /etc/os-release ; echo "${ID}${VERSION_ID}" )
if [ "$os" == "amzn2023" ]; then

  # Fix issues with networking from pods
  sed -i "s/^MACAddressPolicy=.*/MACAddressPolicy=none/" /usr/lib/systemd/network/99-default.link
  systemctl restart systemd-resolved

  # Remove duplicate lines in /etc/resolv.conf
  awk -i inplace '!seen[$0]++'  /etc/resolv.conf || true
fi

mkdir -p /etc/kubernetes/
cat << EOF > /etc/kubernetes/kubeadm-join.yaml
apiVersion: kubeadm.k8s.io/v1beta3
kind: JoinConfiguration
discovery:
  bootstrapToken:
    apiServerEndpoint: {{KUBEADM_CONTROL_PLANE_IP}}:6443
    token: {{KUBEADM_TOKEN}}
    unsafeSkipCAVerification: true
nodeRegistration:
  criSocket: unix:///run/containerd/containerd.sock
  name: {{HOSTNAME_OVERRIDE}}
  kubeletExtraArgs:
    feature-gates: {{FEATURE_GATES}}
    cloud-provider: {{EXTERNAL_CLOUD_PROVIDER}}
    provider-id: {{PROVIDER_ID}}
    node-ip: {{NODE_IP}}
    hostname-override: {{HOSTNAME_OVERRIDE}}
    image-credential-provider-bin-dir: /etc/eks/image-credential-provider/
    image-credential-provider-config: /etc/eks/image-credential-provider/config.json
    resolv-conf: /etc/resolv.conf
EOF

cat <<EOF > /etc/eks/image-credential-provider/config.json
{
  "apiVersion": "kubelet.config.k8s.io/v1",
  "kind": "CredentialProviderConfig",
  "providers": [
    {
      "name": "ecr-credential-provider",
      "matchImages": [
        "*.dkr.ecr.*.amazonaws.com",
        "*.dkr.ecr.*.amazonaws.com.cn",
        "*.dkr.ecr-fips.*.amazonaws.com",
        "*.dkr.ecr.*.c2s.ic.gov",
        "*.dkr.ecr.*.sc2s.sgov.gov"
      ],
      "defaultCacheDuration": "12h",
      "apiVersion": "credentialprovider.kubelet.k8s.io/v1"
    }
  ]
}
EOF

TOKEN=$(curl --request PUT "http://169.254.169.254/latest/api/token" --header "X-aws-ec2-metadata-token-ttl-seconds: 3600" -s)
META_URL=http://169.254.169.254/latest/meta-data
AVAILABILITY_ZONE=$(curl -s $META_URL/placement/availability-zone --header "X-aws-ec2-metadata-token: $TOKEN")
INSTANCE_ID=$(curl -s $META_URL/instance-id --header "X-aws-ec2-metadata-token: $TOKEN")
PROVIDER_ID="aws:///$AVAILABILITY_ZONE/$INSTANCE_ID"
PRIVATE_DNS_NAME=$(curl -s $META_URL/hostname --header "X-aws-ec2-metadata-token: $TOKEN")
NODE_IP=$(curl -s $META_URL/local-ipv4 --header "X-aws-ec2-metadata-token: $TOKEN")

sed -i "s|{{PROVIDER_ID}}|$PROVIDER_ID|g" /etc/kubernetes/kubeadm-join.yaml
sed -i "s|{{HOSTNAME_OVERRIDE}}|$PRIVATE_DNS_NAME|g" /etc/kubernetes/kubeadm-join.yaml
sed -i "s|{{NODE_IP}}|$NODE_IP|g" /etc/kubernetes/kubeadm-join.yaml

# Ensure references to the instance id are resolved properly
echo "$(curl -s -f -m 1 --header "X-aws-ec2-metadata-token: $TOKEN" $META_URL/local-ipv4) $(curl -s -f -m 1 --header "X-aws-ec2-metadata-token: $TOKEN" $META_URL/instance-id/)" | sudo tee -a /etc/hosts

VERSION="v1.28.0"
curl -sSL --fail --retry 5 https://storage.googleapis.com/k8s-artifacts-cri-tools/release/$VERSION/crictl-$VERSION-linux-$ARCH.tar.gz | sudo tar -xvzf - -C /usr/local/bin

RELEASE_VERSION="v0.16.4"
curl -sSL "https://raw.githubusercontent.com/kubernetes/release/${RELEASE_VERSION}/cmd/krel/templates/latest/kubelet/kubelet.service" | sed "s:/usr/bin:/bin:g" | sudo tee /etc/systemd/system/kubelet.service
sudo mkdir -p /etc/systemd/system/kubelet.service.d
curl -sSL "https://raw.githubusercontent.com/kubernetes/release/${RELEASE_VERSION}/cmd/krel/templates/latest/kubeadm/10-kubeadm.conf" | sed "s:/usr/bin:/bin:g" | sudo tee /etc/systemd/system/kubelet.service.d/10-kubeadm.conf
systemctl enable --now kubelet

# shellcheck disable=SC2050
if [[ "{{STAGING_BUCKET}}" =~ ^s3.*  ]]; then
  aws s3 cp "{{STAGING_BUCKET}}/{{STAGING_VERSION}}/kubernetes-server-linux-$ARCH.tar.gz" "kubernetes-server-linux-$ARCH.tar.gz"
elif [[ "{{STAGING_BUCKET}}" =~ ^https.*  ]]; then
  curl -sSLo kubernetes-server-linux-$ARCH.tar.gz --fail --retry 5 "{{STAGING_BUCKET}}/{{STAGING_VERSION}}/kubernetes-server-linux-$ARCH.tar.gz"
else
  aws s3 cp "s3://{{STAGING_BUCKET}}/{{STAGING_VERSION}}/kubernetes-server-linux-$ARCH.tar.gz" "kubernetes-server-linux-$ARCH.tar.gz"
fi

tar -xvzf kubernetes-server-linux-$ARCH.tar.gz
cp ./kubernetes/server/bin/* /usr/local/bin/

if command -v dnf; then
  dnf reinstall runc containerd -y --allowerasing
else
  yum reinstall runc containerd -y
fi

systemctl stop containerd
rm -f /etc/containerd/config.toml
sed -i 's|LimitNOFILE=.*|LimitNOFILE=1048576|' /usr/lib/systemd/system/containerd.service

if [[ -f "/etc/eks/containerd/containerd-config.toml" ]]; then
  cp /etc/eks/containerd/containerd-config.toml /etc/containerd/config.toml
else
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
EOF

# one of the CRI tests needs an extra "test-handler" so append that at the end
cat <<EOF >> /etc/containerd/config.toml
# Setup a runtime with the magic name ("test-handler") used for Kubernetes
# runtime class tests ...
[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.test-handler]
  runtime_type = "io.containerd.runc.v2"
EOF
fi

# fix up sandbox_image if not set properly
sed -i "s/SANDBOX_IMAGE/registry.k8s.io\/pause:3.8/" /etc/containerd/config.toml

# enable nvidia driver if present
if command -v nvidia-ctk; then
  nvidia-ctk runtime configure --runtime=containerd
  sed -i 's/default_runtime_name = .*/default_runtime_name = "nvidia"/' /etc/containerd/config.toml
fi

systemctl start containerd
/usr/bin/containerd --version
/usr/sbin/runc --version

# shellcheck disable=SC2038
find ./kubernetes/server/bin -name "*.tar" -print | xargs -L 1 ctr -n k8s.io images import
# shellcheck disable=SC2016
ctr -n k8s.io images ls -q | grep -e $ARCH | xargs -L 1 -I '{}' /bin/bash -c 'ctr -n k8s.io images tag "{}" "$(echo "{}" | sed s/-'$ARCH':/:/)"'

# shellcheck disable=SC2155
export PATH=$PATH:/usr/local/bin

kubeadm join \
   --ignore-preflight-errors=FileContent--proc-sys-net-bridge-bridge-nf-call-iptables \
   --v 10 \
   --config /etc/kubernetes/kubeadm-join.yaml
