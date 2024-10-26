#!/bin/bash
set -xeu

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

if [ "$(uname -m)" = "arm64" ] || [ "$(uname -m)" = "aarch64" ]; then
  ARCH=arm64
else
  ARCH=amd64
fi

VERSION="v1.27.1"
curl -sSLo /usr/local/bin/ecr-credential-provider --fail --retry 5 "https://artifacts.k8s.io/binaries/cloud-provider-aws/$VERSION/linux/$ARCH/ecr-credential-provider-linux-$ARCH"
chmod +x /usr/local/bin/ecr-credential-provider

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
sudo cp ./kubernetes/server/bin/* /usr/local/bin/

VERSION="v1.27.1"
curl -sSL --fail --retry 5 https://storage.googleapis.com/k8s-artifacts-cri-tools/release/$VERSION/crictl-$VERSION-linux-$ARCH.tar.gz | sudo tar -xvzf - -C /usr/local/bin

TOKEN=$(curl --request PUT "http://169.254.169.254/latest/api/token" --header "X-aws-ec2-metadata-token-ttl-seconds: 3600" -s)
META_URL=http://169.254.169.254/latest/meta-data
# generate the right provider-id and host name needed for external aws cloud provider
AVAILABILITY_ZONE=$(curl -s $META_URL/placement/availability-zone --header "X-aws-ec2-metadata-token: $TOKEN")
INSTANCE_ID=$(curl -s $META_URL/instance-id --header "X-aws-ec2-metadata-token: $TOKEN")
PROVIDER_ID="aws:///$AVAILABILITY_ZONE/$INSTANCE_ID"
PRIVATE_DNS_NAME=$(curl -s $META_URL/hostname --header "X-aws-ec2-metadata-token: $TOKEN")
NODE_IP=$(curl -s $META_URL/local-ipv4 --header "X-aws-ec2-metadata-token: $TOKEN")

sed -i "s|{{PROVIDER_ID}}|$PROVIDER_ID|g" /etc/kubernetes/kubeadm-*.yaml
sed -i "s|{{HOSTNAME_OVERRIDE}}|$PRIVATE_DNS_NAME|g" /etc/kubernetes/kubeadm-*.yaml
sed -i "s|{{NODE_IP}}|$NODE_IP|g" /etc/kubernetes/kubeadm-*.yaml

sudo modprobe br_netfilter
sudo sysctl --system
sudo systemctl daemon-reload && sudo systemctl restart kubelet

sudo ln -s /home/containerd/usr/local/bin/ctr /usr/local/bin/ctr || true
# shellcheck disable=SC2038
find ./kubernetes/server/bin -name "*.tar" -print | xargs -L 1 ctr -n k8s.io images import

# shellcheck disable=SC2016
ctr -n k8s.io images ls -q | grep -e $ARCH | xargs -L 1 -I '{}' /bin/bash -c 'ctr -n k8s.io images tag "{}" "$(echo "{}" | sed s/-'$ARCH':/:/)"'

  KUBE_LOG_LEVEL=${KUBE_LOG_LEVEL:-4}
  sed -i "s|{{KUBE_LOG_LEVEL}}|$KUBE_LOG_LEVEL|g" /etc/kubernetes/kubeadm-init.yaml

# {{KUBEADM_CONTROL_PLANE}} should be "true" or "false"
if [[ ${KUBEADM_CONTROL_PLANE} == true ]]; then
  TOKEN=$(curl --request PUT "http://169.254.169.254/latest/api/token" --header "X-aws-ec2-metadata-token-ttl-seconds: 3600" -s)
  MAC=$(curl -s $META_URL/network/interfaces/macs/ -s --header "X-aws-ec2-metadata-token: $TOKEN" | head -n 1)
  LOCAL_IP=$(curl -s $META_URL/local-ipv4 --header "X-aws-ec2-metadata-token: $TOKEN")
  FIRST_TWO_OCTETS=$(echo $LOCAL_IP | cut -d'.' -f1,2)
  POD_CIDR=$(curl -s $META_URL/network/interfaces/macs/"$MAC"/vpc-ipv4-cidr-blocks --header "X-aws-ec2-metadata-token: $TOKEN" | grep "$FIRST_TWO_OCTETS.")

  sed -i "s|{{BOOTSTRAP_TOKEN}}|{{KUBEADM_TOKEN}}|g" /etc/kubernetes/kubeadm-init.yaml
  EXTRA_SANS=$(curl -s --connect-timeout 3 $META_URL/public-ipv4 --header "X-aws-ec2-metadata-token: $TOKEN")
  sed -i "s|{{EXTRA_SANS}}|$EXTRA_SANS|g" /etc/kubernetes/kubeadm-init.yaml
  KUBERNETES_VERSION=$(kubelet --version | awk '{print $2}')
  sed -i "s|{{KUBERNETES_VERSION}}|$KUBERNETES_VERSION|g" /etc/kubernetes/kubeadm-init.yaml
  sed -i "s|{{POD_CIDR}}|$POD_CIDR|g" /etc/kubernetes/kubeadm-init.yaml

  kubeadm init \
   --v 10 \
   --ignore-preflight-errors=ImagePull \
   --config /etc/kubernetes/kubeadm-init.yaml

  kubeadm init phase upload-certs \
    --v 10 \
    --upload-certs \
    --skip-certificate-key-print \
    --certificate-key "{{KUBEADM_CERTIFICATE_KEY}}"
else
  sed -i "s|{{BOOTSTRAP_TOKEN}}|{{KUBEADM_TOKEN}}|g" /etc/kubernetes/kubeadm-join.yaml
  sed -i "s|{{KUBEADM_CONTROL_PLANE_IP}}|$KUBEADM_CONTROL_PLANE_IP|g" /etc/kubernetes/kubeadm-join.yaml
  kubeadm join \
   --v 10 \
   --config /etc/kubernetes/kubeadm-join.yaml
fi
