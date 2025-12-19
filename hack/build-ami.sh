#!/usr/bin/env bash

set -xeuo pipefail

# Copyright 2016 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

TEST_INFRA_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
${TEST_INFRA_ROOT}/hack/populate-s3.sh

curl -fsSL https://releases.hashicorp.com/packer/1.9.1/packer_1.9.1_linux_amd64.zip | funzip > /usr/local/bin/packer && \
  chmod +x /usr/local/bin/packer

[[ ! -d "$(go env GOPATH)/src/github.com/awslabs/amazon-eks-ami" ]] && \
  mkdir -p "$(go env GOPATH)/src/github.com/awslabs" && \
  git clone https://github.com/awslabs/amazon-eks-ami "$(go env GOPATH)/src/github.com/awslabs/amazon-eks-ami"

# The default branch of this repository will be changed to main from master on February 29, 2024
pushd "$(go env GOPATH)/src/github.com/awslabs/amazon-eks-ami" >/dev/null
  if [[ "$(git rev-parse --abbrev-ref HEAD)" != "main" ]]; then
    git checkout -b main origin/main
  fi
popd

pushd "$(go env GOPATH)/src/k8s.io/kubernetes" >/dev/null
  KUBE_FULL_VERSION=$(hack/print-workspace-status.sh | grep gitVersion | awk '{print $2}')
  KUBE_VERSION=$(echo $KUBE_FULL_VERSION | sed -E 's/v([0-9]+)\.([0-9]+)\.([0-9]+).*/v\1.\2.\3/')
popd
KUBE_DATE=$(date -u +'%Y-%m-%d')

# Generate aws-iam-authenticator binaries
# shellcheck disable=SC2164
pushd "$(go env GOPATH)/src/github.com/awslabs/amazon-eks-ami" >/dev/null
  # Check if AMI already exists before attempting to build
  if [[ -n "${AMI_NAME:-}" ]]; then
    existing_ami=$(aws ec2 describe-images --region=${AWS_REGION:-"us-east-1"} --filters Name=name,Values="$AMI_NAME" --query 'Images[*].[ImageId]' --output text --max-items 1 | head -1)
    if [[ -n "${existing_ami}" ]]; then
      echo "AMI '$AMI_NAME' already exists: ${existing_ami}, skipping build..."
      popd
      exit 0
    fi
  fi

  if [ -f scripts/install-worker.sh ]; then
    sed -i 's/sudo wget .*sha256$//' scripts/install-worker.sh >/dev/null 2>&1 || true
    sed -i 's/sudo rm .*sha256$//' scripts/install-worker.sh >/dev/null 2>&1 || true
    sed -i 's/sudo sha256sum.*$//' scripts/install-worker.sh >/dev/null 2>&1 || true
    sed -i 's/99-no-policy.conf/99-no-policy.invalid/' scripts/install-worker.sh >/dev/null 2>&1 || true
    sed -i 's/.*amazon-ec2-net-utils.*$//' scripts/install-worker.sh >/dev/null 2>&1 || true
    sed -i 's/sudo.*cri-tools.*$//' scripts/install-worker.sh >/dev/null 2>&1 || true

    cat <<< "$(jq --arg bucket ${S3_BUCKET:-'provider-aws-test-infra'} '.binary_bucket_name = $bucket' eks-worker-al2-variables.json)" > eks-worker-al2-variables.json || true
    cat <<< "$(jq --arg bucket_region ${AWS_REGION:-'us-east-1'} '.binary_bucket_region = $bucket_region' eks-worker-al2-variables.json)" > eks-worker-al2-variables.json  || true
    cat <<< "$(jq --arg aws_region ${AWS_REGION:-'us-east-1'} '.aws_region = $aws_region' eks-worker-al2-variables.json)" > eks-worker-al2-variables.json  || true
    cat <<< "$(jq --arg instance_profile ${INSTANCE_PROFILE_NAME:-'packer-instance-profile'} '.iam_instance_profile = $instance_profile' eks-worker-al2-variables.json)" > eks-worker-al2-variables.json || true
    cat <<< "$(jq --arg pause_container_image ${PAUSE_CONTAINER_IMAGE:-'registry.k8s.io/pause:3.10'} '.pause_container_image = $pause_container_image' eks-worker-al2-variables.json)" > eks-worker-al2-variables.json || true
    cat <<< "$(jq --arg containerd_version ${CONTAINERD_VERSION:-"1.7.*"} '.containerd_version = $containerd_version' eks-worker-al2-variables.json)" > eks-worker-al2-variables.json || true

    if [[ ${BUILD_EKS_AMI_OS:-""} == "al2023" ]]; then
      make transform-al2-to-al2023
      export PACKER_DEFAULT_VARIABLE_FILE=eks-worker-al2023-variables.json
      export PACKER_TEMPLATE_FILE=eks-worker-al2023.json
    else
      export PACKER_DEFAULT_VARIABLE_FILE=eks-worker-al2-variables.json
      export PACKER_TEMPLATE_FILE=eks-worker-al2.json
    fi
    if [[ ${BUILD_EKS_AMI_ARCH:-""} == "arm64" ]]; then
      sed -i 's/x86_64/arm64/' ${PACKER_DEFAULT_VARIABLE_FILE}
      sed -i 's/x86_64/arm64/' ${PACKER_TEMPLATE_FILE}
    fi
    make k8s kubernetes_version=${KUBE_VERSION} kubernetes_build_date=${KUBE_DATE} \
      pull_cni_from_github=true arch=${BUILD_EKS_AMI_ARCH:-"x86_64"} || true
  else
    # disable sha256 check
    sed -i 's/sudo wget .*sha256$//' templates/*/provisioners/install-worker.sh >/dev/null 2>&1 || true
    sed -i 's/sudo rm .*sha256$//' templates/*/provisioners/install-worker.sh >/dev/null 2>&1 || true
    sed -i 's/sudo sha256sum.*$//' templates/*/provisioners/install-worker.sh >/dev/null 2>&1 || true
    sed -i 's/99-no-policy.conf/99-no-policy.invalid/' templates/*/provisioners/install-worker.sh >/dev/null 2>&1 || true
    sed -i 's/.*amazon-ec2-net-utils.*$//' templates/*/provisioners/install-worker.sh >/dev/null 2>&1 || true
    sed -i 's/sudo.*cri-tools.*$//' templates/*/provisioners/install-worker.sh >/dev/null 2>&1 || true

    cat <<< "$(jq --arg bucket ${S3_BUCKET:-'provider-aws-test-infra'} '.binary_bucket_name = $bucket' templates/al2/variables-default.json)" > templates/al2/variables-default.json || true
    cat <<< "$(jq --arg bucket_region ${AWS_REGION:-'us-east-1'} '.binary_bucket_region = $bucket_region' templates/al2/variables-default.json)" > templates/al2/variables-default.json || true
    cat <<< "$(jq --arg aws_region ${AWS_REGION:-'us-east-1'} '.aws_region = $aws_region' templates/al2/variables-default.json)" > templates/al2/variables-default.json || true
    cat <<< "$(jq --arg instance_profile ${INSTANCE_PROFILE_NAME:-'packer-instance-profile'} '.iam_instance_profile = $instance_profile' templates/al2/variables-default.json)" > templates/al2/variables-default.json || true
    cat <<< "$(jq --arg pause_container_image ${PAUSE_CONTAINER_IMAGE:-'registry.k8s.io/pause:3.10'} '.pause_container_image = $pause_container_image' templates/al2/variables-default.json)" > templates/al2/variables-default.json || true
    cat <<< "$(jq --arg containerd_version ${CONTAINERD_VERSION:-"1.7.*"} '.containerd_version = $containerd_version' templates/al2/variables-default.json)" > templates/al2/variables-default.json || true


    cat <<< "$(jq --arg bucket ${S3_BUCKET:-'provider-aws-test-infra'} '.binary_bucket_name = $bucket' templates/al2023/variables-default.json)" > templates/al2023/variables-default.json || true
    cat <<< "$(jq --arg bucket_region ${AWS_REGION:-'us-east-1'} '.binary_bucket_region = $bucket_region' templates/al2023/variables-default.json)" > templates/al2023/variables-default.json || true
    cat <<< "$(jq --arg aws_region ${AWS_REGION:-'us-east-1'} '.aws_region = $aws_region' templates/al2023/variables-default.json)" > templates/al2023/variables-default.json || true
    cat <<< "$(jq --arg instance_profile ${INSTANCE_PROFILE_NAME:-'packer-instance-profile'} '.iam_instance_profile = $instance_profile' templates/al2023/variables-default.json)" > templates/al2023/variables-default.json || true
    cat <<< "$(jq --arg pause_container_image ${PAUSE_CONTAINER_IMAGE:-'registry.k8s.io/pause:3.10'} '.pause_container_image = $pause_container_image' templates/al2023/variables-default.json)" > templates/al2023/variables-default.json || true
    cat <<< "$(jq --arg containerd_version ${CONTAINERD_VERSION:-"1.7.*"} '.containerd_version = $containerd_version' templates/al2023/variables-default.json)" > templates/al2023/variables-default.json || true

    make k8s kubernetes_version=${KUBE_VERSION} kubernetes_build_date=${KUBE_DATE} \
      pull_cni_from_github=true arch=${BUILD_EKS_AMI_ARCH:-"x86_64"} os_distro=${BUILD_EKS_AMI_OS:-"al2023"} || true
  fi
  ami_id=$(aws ec2 describe-images --region=${AWS_REGION:-"us-east-1"} --filters Name=name,Values="$AMI_NAME" --query 'Images[*].[ImageId]' --output text --max-items 1 | head -1)
  if [ -z "${ami_id}" ] ; then
    echo "unable to build ${AMI_NAME}, please see packer logs above..."
    exit 1
  fi
# shellcheck disable=SC2164
popd
