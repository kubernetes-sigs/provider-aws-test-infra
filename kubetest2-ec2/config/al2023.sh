#!/bin/bash

set -o xtrace
set -xeuo pipefail

os=$( . /etc/os-release ; echo "${ID}${VERSION_ID}" )

# Set the maximum number of retries
MAX_RETRIES=5

# Function to run DNF/yum command with retries
install_packages_with_retry() {
    set +e
    local attempt=1

    EXTRAS=""
    if [ "$os" == "amzn2023" ]; then
      EXTRAS="iptables-nft"
    fi

    while [ $attempt -le $MAX_RETRIES ]; do
        echo "Attempt $attempt of $MAX_RETRIES"

        # Run the DNF command
        DNF=""
        DNF_ARGS=""
        if command -v dnf; then
          DNF="dnf"
        else
          DNF="yum"
          DNF_ARGS="--enablerepo=amzn2extra-docker"
        fi

        $DNF clean all
        $DNF makecache
        $DNF update -y

        $DNF $DNF_ARGS install -y \
          runc \
          containerd \
          git \
          aws-cfn-bootstrap \
          chrony \
          conntrack \
          ec2-instance-connect \
          ethtool \
          ipvsadm \
          jq \
          nfs-utils \
          socat \
          unzip \
          wget \
          mdadm \
          pigz $EXTRAS

        # Check if the command was successful
        if [ $? -eq 0 ]; then
            echo "DNF/YUM command succeeded"
            return 0
        else
            echo "DNF/YUM command failed. Retrying in 5 seconds..."
            sleep 5
            ((attempt++))
        fi
    done

    echo "DNF/YUM command failed after $MAX_RETRIES attempts"
    set -e
    return 1
}

wait_for_update() {
    local bucket="$1" key="$2"
    local previous_etag previous_size
    previous_etag=$(get_etag "$bucket" "$key")
    previous_size=$(get_size "$bucket" "$key")

    echo "Monitoring updates for $bucket/$key..."
    SECONDS=0
    while ((SECONDS < 300)); do
        sleep 15
        local current_etag current_size
        current_etag=$(get_etag "$bucket" "$key")
        current_size=$(get_size "$bucket" "$key")

        if [[ "$current_etag" == "$previous_etag" && "$current_size" == "$previous_size" ]]; then
            echo "File is stable. Ready for download."
            return 0
        fi

        previous_etag=$current_etag
        previous_size=$current_size
    done

    echo "Timeout reached. File may still be updating."
    return 2
}

get_etag() {
    aws s3api head-object --bucket "$1" --key "$2" --query ETag --output text
}

get_size() {
    aws s3api head-object --bucket "$1" --key "$2" --query ContentLength --output text
}

# Start with a clean slate
# Note that the `iptables -P FORWARD ACCEPT` piece is load bearing! https://github.com/search?q=repo%3Aawslabs%2Famazon-eks-ami+%22iptables+-P%22&type=code
iptables -F && iptables -X  && iptables -t nat -F  && iptables -t nat -X && iptables -t mangle -F  && iptables -t mangle -X  && iptables -P INPUT ACCEPT  && iptables -P FORWARD ACCEPT -w 5 && iptables -P OUTPUT ACCEPT -w 5

if [ "$(uname -m)" = "arm64" ] || [ "$(uname -m)" = "aarch64" ]; then
  ARCH=arm64
else
  ARCH=amd64
fi

os=$( . /etc/os-release ; echo "${ID}${VERSION_ID}" )
if [ "$os" == "amzn2023" ]; then

  # Fix issues with networking from pods
  sed -i "s/^.*ReadEtcHosts.*/ReadEtcHosts=no/" /etc/systemd/resolved.conf
  sed -i "s/^MACAddressPolicy=.*/MACAddressPolicy=none/" /usr/lib/systemd/network/99-default.link
  systemctl restart systemd-resolved

  # Remove duplicate lines in /etc/resolv.conf
  awk -i inplace '!seen[$0]++'  /etc/resolv.conf || true
  RESOLVE_CONF=/run/systemd/resolve/resolv.conf
else
  RESOLVE_CONF=/etc/resolv.conf
fi

mkdir -p /etc/kubernetes/
mkdir -p /etc/kubernetes/manifests
mkdir -p /etc/eks/image-credential-provider/
mkdir -p /var/log/pods/
chmod -R a+rx /var/log/pods/
mkdir -p /var/log/containers/
chmod -R a+rx /var/log/containers/

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
    resolv-conf: $RESOLVE_CONF
    system-cgroups: /system.slice
    runtime-cgroups: /runtime.slice
    kubelet-cgroups: /runtime.slice
    cgroup-root: /
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

cat << EOF | sudo tee /etc/systemd/system/runtime.slice
[Unit]
Description=Kubernetes and container runtime slice
Documentation=man:systemd.special(7)
Before=slices.target
EOF

RELEASE_VERSION="v0.16.4"
curl -sSL "https://raw.githubusercontent.com/kubernetes/release/${RELEASE_VERSION}/cmd/krel/templates/latest/kubelet/kubelet.service" | sed "s:/usr/bin:/bin:g" | sudo tee /etc/systemd/system/kubelet.service
sudo mkdir -p /etc/systemd/system/kubelet.service.d
curl -sSL "https://raw.githubusercontent.com/kubernetes/release/${RELEASE_VERSION}/cmd/krel/templates/latest/kubeadm/10-kubeadm.conf" | sed "s:/usr/bin:/bin:g" | sudo tee /etc/systemd/system/kubelet.service.d/10-kubeadm.conf

cat << EOF | sudo tee /etc/systemd/system/kubelet.service.d/00-runtime-slice.conf
[Service]
Slice=runtime.slice
Restart=on-failure
RestartForceExitStatus=SIGPIPE
RestartSec=5
KillMode=process
CPUAccounting=true
MemoryAccounting=true
EOF

VERSION="v1.27.1"
curl -sSLo /usr/local/bin/ecr-credential-provider --fail --retry 5 "https://artifacts.k8s.io/binaries/cloud-provider-aws/$VERSION/linux/$ARCH/ecr-credential-provider-linux-$ARCH"
chmod +x /usr/local/bin/ecr-credential-provider
ln -s /usr/local/bin/ecr-credential-provider /etc/eks/image-credential-provider/ || true

# Download and configure CNI
cni_bin_dir="/opt/cni/bin"

CNI_VERSION=v1.2.0 &&\
mkdir -p ${cni_bin_dir} &&\
curl -fsSL https://github.com/containernetworking/plugins/releases/download/${CNI_VERSION}/cni-plugins-linux-${ARCH}-${CNI_VERSION}.tgz \
    | tar xfz - -C ${cni_bin_dir}


# shellcheck disable=SC2050
if [[ "{{STAGING_BUCKET}}" =~ ^https.*  ]]; then
  curl -sSLo kubernetes-server-linux-$ARCH.tar.gz --fail --retry 5 "{{STAGING_BUCKET}}/{{STAGING_VERSION}}/kubernetes-server-linux-$ARCH.tar.gz"
else
  BUCKET="{{STAGING_BUCKET}}"
  # Strip out 's3://' prefix if it exists
  if [[ "$BUCKET" =~ ^s3:// ]]; then
    BUCKET="${BUCKET#s3://}"
  fi
  VERSION="{{STAGING_VERSION}}"
  FILE_NAME="kubernetes-server-linux-$ARCH.tar.gz"
  KEY="$VERSION/$FILE_NAME"

  echo "Waiting to see if s3://$BUCKET/$KEY is being updated..."
  wait_for_update "$BUCKET" "$KEY"

  aws s3 cp --no-progress "s3://$BUCKET/$KEY" "$FILE_NAME"
fi

tar -xvzf kubernetes-server-linux-$ARCH.tar.gz
cp ./kubernetes/server/bin/* /usr/local/bin/
find /usr/local/bin/ -type f -executable -exec ln -s {} /usr/bin \; || true

install_packages_with_retry

cat << EOF | sudo tee -a /etc/sysctl.d/99-kubernetes-cri.conf
net.bridge.bridge-nf-call-ip6tables = 1
net.bridge.bridge-nf-call-iptables = 1
net.ipv4.ip_forward = 1
EOF
sysctl --system

systemctl stop containerd
rm -f /etc/containerd/config.toml

cat << EOF | sudo tee /etc/systemd/system/containerd.service.d/00-runtime-slice.conf
[Service]
Slice=runtime.slice
EOF

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
