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

set -xeuo pipefail

pushd "$(go env GOPATH)/src/k8s.io/kubernetes" >/dev/null
  KUBE_MINOR_VERSION=$(hack/print-workspace-status.sh | grep gitVersion | awk '{print $2}' | sed -E 's/v([0-9]+)\.([0-9]+).*/v\1.\2/')
popd
TODAYS_DATE=$(date -u +'%Y%m%d')
TEST_INFRA_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

# x86_64 does not affect the name of the image, just arm64
# see https://github.com/awslabs/amazon-eks-ami/blob/master/Makefile#L34-L41
build_eks_arch=""
if [[ ${BUILD_EKS_AMI_ARCH:-""} == "arm64" ]]; then
  build_eks_arch="arm64-"
fi

build_eks_ami=${BUILD_EKS_AMI:-"false"}
if [[ ${build_eks_ami} != "false" ]]; then
  instance_type=${INSTANCE_TYPE:-"m6a.large"}
  if [[ ${BUILD_EKS_AMI_ARCH:-""} == "arm64" ]]; then
    instance_type=${INSTANCE_TYPE:-"m6g.large"}
  fi
  # Default to AL2023 for all builds
  user_data_file="userdata-al2023.sh"
  AMI_NAME="amazon-eks-al2023-${build_eks_arch}node-${KUBE_MINOR_VERSION}-v${TODAYS_DATE}"
  ami_id=$(aws ec2 describe-images --region=${AWS_REGION:-"us-east-1"} --filters Name=name,Values="$AMI_NAME"  --query 'Images[*].[ImageId]' --output text --max-items 1 | head -1)
  if [ -z "${ami_id}" ] ; then
    export AMI_NAME
    ${TEST_INFRA_ROOT}/hack/build-ami.sh
  else
    echo "found existing ami : ${ami_id} skipping building a new AMI..."
  fi
  ami_id=$(aws ec2 describe-images --region=${AWS_REGION:-"us-east-1"} --filters Name=name,Values="$AMI_NAME"  --query 'Images[*].[ImageId]' --output text --max-items 1 | head -1)
  aws ec2 describe-images --region=${AWS_REGION:-"us-east-1"} --image-ids ${ami_id}
  cat > ${TEST_INFRA_ROOT}/config/aws-instance-eks.yaml <<EOF
images:
  eks-ami-daily:
    ami_id: ${ami_id}
    instance_type: ${instance_type}
    user_data_file: ${user_data_file}
EOF
fi

KUBE_ROOT="${KUBE_ROOT:-"$(go env GOPATH)/src/k8s.io/kubernetes"}"
export KUBE_ROOT

# start the cache mutation detector by default so that cache mutators will be found
KUBE_CACHE_MUTATION_DETECTOR="${KUBE_CACHE_MUTATION_DETECTOR:-true}"
export KUBE_CACHE_MUTATION_DETECTOR

# panic the server on watch decode errors since they are considered coder mistakes
KUBE_PANIC_WATCH_DECODE_ERROR="${KUBE_PANIC_WATCH_DECODE_ERROR:-true}"
export KUBE_PANIC_WATCH_DECODE_ERROR

focus=${FOCUS:-""}
skip=${SKIP-"\[Flaky\]|\[Slow\]|\[Serial\]"}
# The number of tests that can run in parallel depends on what tests
# are running and on the size of the node. Too many, and tests will
# fail due to resource contention. 4 is a reasonable default to avoid
# network and resource contention issues on AWS instances.
# Currently, parallelism only affects when REMOTE=true. For local test,
# ginkgo default parallelism (cores - 1) is used.
parallelism=${PARALLELISM:-4}
artifacts="${ARTIFACTS:-"/tmp/_artifacts/$(date +%y%m%dT%H%M%S)"}"
container_runtime_endpoint=${CONTAINER_RUNTIME_ENDPOINT:-"unix:///run/containerd/containerd.sock"}
image_service_endpoint=${IMAGE_SERVICE_ENDPOINT:-""}
run_until_failure=${RUN_UNTIL_FAILURE:-"false"}
test_args=${TEST_ARGS:-""}
timeout_arg=""
system_spec_name=${SYSTEM_SPEC_NAME:-}
extra_envs=${EXTRA_ENVS:-}
runtime_config=${RUNTIME_CONFIG:-}
ssh_user=${SSH_USER:-"${USER}"}
ssh_key=${SSH_KEY:-}
ssh_options=${SSH_OPTIONS:-}
kubelet_config_file=${KUBELET_CONFIG_FILE:-"test/e2e_node/jenkins/default-kubelet-config.yaml"}
instance_type=${INSTANCE_TYPE:-}

# Parse the flags to pass to ginkgo
ginkgoflags="-timeout=24h"

if [[ ${focus} != "" ]]; then
  ginkgoflags="${ginkgoflags} -focus=\"${focus}\" "
  if [[ "${focus}" == *"Serial"* ]]; then
    parallelism=1
  fi
fi

if [[ ${parallelism} -ge 1 ]]; then
  ginkgoflags="${ginkgoflags} -nodes=${parallelism} "
fi

if [[ ${skip} != "" ]]; then
  ginkgoflags="${ginkgoflags} -skip=\"${skip}\" "
fi

if [[ ${run_until_failure} == "true" ]]; then
  ginkgoflags="${ginkgoflags} --until-it-fails=true "
fi

if [[ "${container_runtime_endpoint}" =~ /containerd.sock$ ]]; then
  test_args+=" --kubelet-flags=\"--runtime-cgroups=/system.slice/containerd.service\" ${test_args}"
fi

# Setup the directory to copy test artifacts (logs, junit.xml, etc) from remote host to local host
if [ ! -d "${artifacts}" ]; then
  echo "Creating artifacts directory at ${artifacts}"
  mkdir -p "${artifacts}"
fi
echo "Test artifacts will be written to ${artifacts}"

if [[ -n ${container_runtime_endpoint} ]] ; then
  test_args="--container-runtime-endpoint=${container_runtime_endpoint} ${test_args}"
fi
if [[ -n ${image_service_endpoint} ]] ; then
  test_args="--image-service-endpoint=${image_service_endpoint} ${test_args}"
fi

if [[ "${test_args}" != *"prepull-images"* ]]; then
  test_args="--prepull-images=${PREPULL_IMAGES:-false}  ${test_args}"
fi

if [[ "${test_args}" != *"server-start-timeout"* ]]; then
  test_args="--server-start-timeout=${SERVER_START_TIMEOUT:-3m}  ${test_args}"
fi

hosts=${HOSTS:-""}
images=${IMAGES:-""}
image_config_file=${IMAGE_CONFIG_FILE:-""}
image_config_dir=${IMAGE_CONFIG_DIR:-""}
use_dockerized_build=${USE_DOCKERIZED_BUILD:-"false"}
target_build_arch=${TARGET_BUILD_ARCH:-""}
runtime_config=${RUNTIME_CONFIG:-""}
instance_prefix=${INSTANCE_PREFIX:-"test"}
cleanup=${CLEANUP:-"true"}
delete_instances=${DELETE_INSTANCES:-"true"}
instance_profile=${INSTANCE_PROFILE:-""}
user_data_file=${USER_DATA_FILE:-""}
test_suite=${TEST_SUITE:-"default"}
if [[ -n "${TIMEOUT:-"4h"}" ]] ; then
  timeout_arg="--test-timeout=${TIMEOUT:-"4h"}"
fi

# get the account ID
account=$(aws sts get-caller-identity --query Account --output text)
if [[ ${account} == "" ]]; then
  echo "Could not find AWS account ID"
  exit 1
fi

# Use cluster.local as default dns-domain
test_args='--dns-domain="'${KUBE_DNS_DOMAIN:-cluster.local}'" '${test_args}
test_args='--kubelet-flags="--cluster-domain='${KUBE_DNS_DOMAIN:-cluster.local}'" '${test_args}

region=${AWS_REGION:-$(aws configure get region)}
if [[ ${region} == "" ]]; then
    echo "Could not find AWS region specified"
    exit 1
fi
# Output the configuration we will try to run
echo "Running tests remotely using"
echo "Account: ${account}"
echo "Region: ${region}"
if [[ -n ${hosts} ]]; then
  echo "Hosts: ${hosts}"
fi
if [[ -n ${images} ]]; then
  echo "Images: ${images}"
fi
echo "SSH User: ${ssh_user}"
if [[ -n ${ssh_key} ]]; then
  echo "SSH Key: ${ssh_key}"
fi
if [[ -n ${ssh_options} ]]; then
  echo "SSH Options: ${ssh_options}"
fi
echo "Ginkgo Flags: ${ginkgoflags}"
if [[ -n ${image_config_file} ]]; then
  echo "Image Config File: ${image_config_dir}/${image_config_file}"
  cat "${image_config_dir}/${image_config_file}"
fi
if [[ -n ${instance_type} ]]; then
  echo "Instance Type: ${instance_type}"
fi
echo "Kubelet Config File: ${kubelet_config_file}"
echo "Kubernetes directory: ${KUBE_ROOT}"

export KUBE_STATIC_OVERRIDES=kubelet

source "${KUBE_ROOT}/hack/lib/version.sh"
source "${KUBE_ROOT}/hack/lib/util.sh"
source "${KUBE_ROOT}/hack/lib/init.sh"

# ensure we use the right golang version
pushd "${KUBE_ROOT}"
kube::golang::setup_env
popd

# Build the runner with
go build -o e2e_node_runner_remote -ldflags "$(kube::version::ldflags)" ./test/e2e_node/runner/remote

# Invoke the runner
./e2e_node_runner_remote --mode="aws" --vmodule=*=4 \
  --ssh-env="aws" --ssh-key="${ssh_key}" --ssh-options="${ssh_options}" --ssh-user="${ssh_user}" \
  --instance-profile="${instance_profile}" --hosts="${hosts}" --cleanup="${cleanup}" \
  --results-dir="${artifacts}" --ginkgo-flags="${ginkgoflags}" --runtime-config="${runtime_config}" \
  --instance-name-prefix="${instance_prefix}" --user-data-file="${user_data_file}" \
  --delete-instances="${delete_instances}" --test_args="${test_args}" --images="${images}" \
  --image-config-file="${image_config_file}" --system-spec-name="${system_spec_name}" \
  --runtime-config="${runtime_config}" --image-config-dir="${image_config_dir}" --region="${region}" \
  --use-dockerized-build="${use_dockerized_build}" --instance-type="${instance_type}" \
  --target-build-arch="${target_build_arch}" \
  --extra-envs="${extra_envs}" --kubelet-config-file="${kubelet_config_file}" --test-suite="${test_suite}" \
  "${timeout_arg}" \
  2>&1 | tee -i "${artifacts}/build-log.txt"

result=${PIPESTATUS[0]} # capture the exit code of the first cmd in pipe.
echo ">> go run exited with ${result} at $(date)"
echo ""
if [[ $result -eq 0 ]]; then
  echo "test-e2e-node.sh: SUCCESS"
else
  echo "test-e2e-node.sh: FAIL"
fi

exit "$result"
