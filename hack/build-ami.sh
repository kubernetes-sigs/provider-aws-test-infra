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

pushd "$(go env GOPATH)/src/k8s.io/kubernetes" >/dev/null
  KUBE_FULL_VERSION=$(hack/print-workspace-status.sh | grep gitVersion | awk '{print $2}')
  KUBE_VERSION=$(echo $KUBE_FULL_VERSION | sed -E 's/v([0-9]+)\.([0-9]+)\.([0-9]+).*/v\1.\2.\3/')
popd
KUBE_DATE=$(date -u +'%Y-%m-%d')

# Generate aws-iam-authenticator binaries
# shellcheck disable=SC2164
pushd "$(go env GOPATH)/src/github.com/awslabs/amazon-eks-ami" >/dev/null
  sed -i 's/amazon-eks/provider-aws-test-infra/' eks-worker-al2-variables.json
  sed -i 's/us-west-2/us-east-1/' eks-worker-al2-variables.json
  if [[ ${BUILD_EKS_AMI_OS:-""} == "al2023" ]]; then
    make transform-al2-to-al2023
    export PACKER_DEFAULT_VARIABLE_FILE=eks-worker-al2023-variables.json
    export PACKER_TEMPLATE_FILE=eks-worker-al2023.json
  fi
  if [[ ${BUILD_EKS_AMI_ARCH:-""} == "arm64" ]]; then
    sed -i 's/x86_64/arm64/' ${PACKER_DEFAULT_VARIABLE_FILE}
    sed -i 's/x86_64/arm64/' ${PACKER_TEMPLATE_FILE}
  fi
  make k8s kubernetes_version=${KUBE_VERSION} kubernetes_build_date=${KUBE_DATE} \
    pull_cni_from_github=true arch=${BUILD_EKS_AMI_ARCH:-"x86_64"}
# shellcheck disable=SC2164
popd