#!/usr/bin/env bash

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

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
${ROOT}/hack/populate-s3.sh

curl -fsSL https://releases.hashicorp.com/packer/1.9.1/packer_1.9.1_linux_amd64.zip | funzip > /usr/local/bin/packer && \
  chmod +x /usr/local/bin/packer

[[ ! -d "$(go env GOPATH)/src/github.com/awslabs/amazon-eks-ami" ]] && \
  mkdir -p "$(go env GOPATH)/src/github.com/awslabs" && \
  git clone https://github.com/awslabs/amazon-eks-ami "$(go env GOPATH)/src/github.com/awslabs/amazon-eks-ami"

# Generate aws-iam-authenticator binaries
# shellcheck disable=SC2164
pushd "$(go env GOPATH)/src/github.com/awslabs/amazon-eks-ami" >/dev/null
  sed -i 's/amazon-eks/provider-aws-test-infra/' eks-worker-al2-variables.json
  sed -i 's/us-west-2/us-east-1/' eks-worker-al2-variables.json
  make k8s kubernetes_version=v1.28.0 kubernetes_build_date=2023-07-04 pull_cni_from_github=true
# shellcheck disable=SC2164
popd