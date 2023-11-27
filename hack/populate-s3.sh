#!/bin/bash

set -xeuo pipefail

TEST_INFRA_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
mkdir -p "${TEST_INFRA_ROOT}/_output"

if [[ -z "$(which aarch64-linux-gnu-gcc)" || -z "$(which x86_64-linux-gnu-gcc)" ]]; then
  echo "Can't find aarch64-linux-gnu-gcc x86_64-linux-gnu-gcc or in PATH, please install and retry"
  if [[ -n "$(command -v lsb_release)" && $(lsb_release -si) == "Ubuntu" ]]; then
    echo "using : apt install -y gcc-aarch64-linux-gnu"
    [[ "$(id -u)" == "0" ]] && apt update && apt install -y gcc-aarch64-linux-gnu
  elif [[ -n "$(command -v lsb_release)" && $(lsb_release -si) == "Debian" ]]; then
    echo "using : apt install -y gcc-aarch64-linux-gnu"
    [[ "$(id -u)" == "0" ]] && apt update && apt install -y gcc-aarch64-linux-gnu
  elif [[ $OSTYPE == 'darwin'* ]]; then
    echo "using : brew tap messense/macos-cross-toolchains && brew install x86_64-unknown-linux-gnu aarch64-unknown-linux-gnu"
  fi
fi

KUBE_DATE=$(date -u +'%Y-%m-%d')

# Generate kubernetes binaries
pushd "$(go env GOPATH)/src/k8s.io/kubernetes" >/dev/null

  KUBE_FULL_VERSION=$(hack/print-workspace-status.sh | grep gitVersion | awk '{print $2}')
  KUBE_VERSION=$(echo $KUBE_FULL_VERSION | sed -E 's/v([0-9]+)\.([0-9]+)\.([0-9]+).*/v\1.\2.\3/')
  BIN_DIR="${TEST_INFRA_ROOT}/_output/${KUBE_VERSION}/${KUBE_DATE}/bin"

  make kubectl KUBE_BUILD_PLATFORMS="darwin/amd64" && \
    make kubectl KUBE_BUILD_PLATFORMS="darwin/arm64" && \
    make kubectl kubelet kube-proxy KUBE_STATIC_OVERRIDES="kubelet" KUBE_BUILD_PLATFORMS="linux/arm64" && \
    make kubectl kubelet kube-proxy KUBE_STATIC_OVERRIDES="kubelet" KUBE_BUILD_PLATFORMS="linux/amd64" && \
    make kubectl kubelet kube-proxy KUBE_STATIC_OVERRIDES="kubelet" KUBE_BUILD_PLATFORMS="windows/amd64"

  mkdir -p "${BIN_DIR}/darwin/amd64"
  cp "_output/local/bin/darwin/amd64/kubectl" "${BIN_DIR}/darwin/amd64"

  mkdir -p "${BIN_DIR}/darwin/arm64"
  cp "_output/local/bin/darwin/arm64/kubectl" "${BIN_DIR}/darwin/arm64"

  mkdir -p "${BIN_DIR}/linux/amd64"
  cp "_output/local/bin/linux/amd64/kubectl" "${BIN_DIR}/linux/amd64"
  cp "_output/local/bin/linux/amd64/kube-proxy" "${BIN_DIR}/linux/amd64"
  cp "_output/local/bin/linux/amd64/kubelet" "${BIN_DIR}/linux/amd64"

  mkdir -p "${BIN_DIR}/linux/arm64"
  cp "_output/local/bin/linux/arm64/kubectl" "${BIN_DIR}/linux/arm64"
  cp "_output/local/bin/linux/arm64/kube-proxy" "${BIN_DIR}/linux/arm64"
  cp "_output/local/bin/linux/arm64/kubelet" "${BIN_DIR}/linux/arm64"

  mkdir -p "${BIN_DIR}/windows/amd64"
  cp "_output/local/bin/windows/amd64/kubectl.exe" "${BIN_DIR}/windows/amd64"
  cp "_output/local/bin/windows/amd64/kube-proxy.exe" "${BIN_DIR}/windows/amd64"
  cp "_output/local/bin/windows/amd64/kubelet.exe" "${BIN_DIR}/windows/amd64"

popd

[[ ! -d "$(go env GOPATH)/src/sigs.k8s.io/aws-iam-authenticator" ]] && \
  git clone https://github.com/kubernetes-sigs/aws-iam-authenticator "$(go env GOPATH)/src/sigs.k8s.io/aws-iam-authenticator"

# Generate aws-iam-authenticator binaries
pushd "$(go env GOPATH)/src/sigs.k8s.io/aws-iam-authenticator" >/dev/null
  AUTHENTICATOR_VERSION=$(git describe --tags `git rev-list --tags --max-count=1`)
  echo ${AUTHENTICATOR_VERSION/#v} > version.txt
  make build-all-bins

  cp "_output/bin/aws-iam-authenticator_${AUTHENTICATOR_VERSION}_darwin_amd64" "${BIN_DIR}/darwin/amd64/aws-iam-authenticator"
  #cp "${TEST_INFRA_ROOT}/_output/local/bin/darwin/arm64/aws-iam-authenticator" "${BIN_DIR}/darwin/arm64/aws-iam-authenticator"
  cp "_output/bin/aws-iam-authenticator_${AUTHENTICATOR_VERSION}_linux_amd64" "${BIN_DIR}/linux/amd64/aws-iam-authenticator"
  cp "_output/bin/aws-iam-authenticator_${AUTHENTICATOR_VERSION}_linux_arm64" "${BIN_DIR}/linux/arm64/aws-iam-authenticator"
  cp "_output/bin/aws-iam-authenticator_${AUTHENTICATOR_VERSION}_windows_amd64.exe" "${BIN_DIR}/windows/amd64/aws-iam-authenticator.exe"

popd

[[ ! -d "$(go env GOPATH)/src/k8s.io/cloud-provider-aws" ]] && \
  git clone https://github.com/kubernetes/cloud-provider-aws "$(go env GOPATH)/src/k8s.io/cloud-provider-aws"

# Generate cloud-provider-aws binaries
pushd "$(go env GOPATH)/src/k8s.io/cloud-provider-aws" >/dev/null
  make crossbuild-ecr-credential-provider

  cp "ecr-credential-provider-linux-amd64" "${BIN_DIR}/linux/amd64/ecr-credential-provider"
  cp "ecr-credential-provider-linux-arm64" "${BIN_DIR}/linux/arm64/ecr-credential-provider"
  cp "ecr-credential-provider-windows-amd64" "${BIN_DIR}/windows/amd64/ecr-credential-provider"
popd

curl -sL https://github.com/containernetworking/cni/releases/download/v0.6.0/cni-amd64-v0.6.0.tgz -o "${BIN_DIR}/linux/amd64/cni-amd64-v0.6.0.tgz"
curl -sL https://github.com/containernetworking/cni/releases/download/v0.6.0/cni-arm64-v0.6.0.tgz -o "${BIN_DIR}/linux/arm64/cni-arm64-v0.6.0.tgz"

curl -sL https://github.com/containernetworking/plugins/releases/download/v0.8.6/cni-plugins-linux-amd64-v0.8.6.tgz -o "${BIN_DIR}/linux/amd64/cni-plugins-linux-amd64-v0.8.6.tgz"
curl -sL https://github.com/containernetworking/plugins/releases/download/v0.8.6/cni-plugins-linux-amd64-v0.8.6.tgz -o "${BIN_DIR}/linux/arm64/cni-plugins-linux-arm64-v0.8.6.tgz"

rm -rf _output/local/go/cache
rm -rf _output/local/go/src
pushd _output >/dev/null
  find . -name "*.sha256*" -delete
  find . -name "*.sha1*" -delete
  find . -name "*.md5*" -delete
  for f in $(find . -type f | sort); do
      dirname=$(dirname $f)
      filename=$(basename $f)
      pushd $dirname >/dev/null
        shasum -a 1 "$filename" > "$filename".sha1 || sha1sum "$filename" > "$filename".sha1;
        shasum -a 256 "$filename" > "$filename".sha256 || sha256sum"$filename" > "$filename".sha256;
        md5sum "$filename" > "$filename".md5;
      popd
  done
popd

S3_BUCKET=${S3_BUCKET:-"provider-aws-test-infra"}
aws s3 sync _output/ "s3://${S3_BUCKET}/"
