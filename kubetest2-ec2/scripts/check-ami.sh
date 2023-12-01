#!/bin/bash

set -x

AWS_REGION=${AWS_REGION:-"us-east-1"}

build_eks_arch=""
TARGET_BUILD_ARCH="linux/amd64"
if [[ ${BUILD_EKS_AMI_ARCH:-""} == "arm64" ]]; then
  build_eks_arch="arm64-"
  # shellcheck disable=SC2034
  AMI_TARGET_BUILD_ARCH="linux/arm64"
fi
# shellcheck disable=SC2164
pushd "$(go env GOPATH)/src/k8s.io/kubernetes" >/dev/null
  KUBE_MINOR_VERSION=$(hack/print-workspace-status.sh | grep gitVersion | awk '{print $2}' | sed -E 's/v([0-9]+)\.([0-9]+).*/v\1.\2/')
# shellcheck disable=SC2164
popd
TODAYS_DATE=$(date -u +'%Y%m%d')
AMI_VERSION="v$TODAYS_DATE"
AMI_NAME="amazon-eks-al2023-${build_eks_arch}node-${KUBE_MINOR_VERSION}-v${TODAYS_DATE}"
AMI_ID=$(aws ec2 describe-images --region=${AWS_REGION} --filters "Name=name,Values=$AMI_NAME" --query 'Images[*].[ImageId]' --output text --max-items 1 | head -1)

if [ -z "$AMI_ID" ] ; then
  export AMI_NAME
  export AMI_VERSION
  # shellcheck disable=SC2046
  $(go env GOPATH)/src/sigs.k8s.io/provider-aws-test-infra/hack/build-ami.sh
  AMI_ID=$(aws ec2 describe-images --region=${AWS_REGION} --filters "Name=name,Values=$AMI_NAME" --query 'Images[*].[ImageId]' --output text --max-items 1 | head -1)
  if [ -z "$AMI_ID" ] ; then
    echo "ami build failed, exiting..."
    exit 1
  fi
else
  echo "found existing ami : $AMI_ID skipping building a new AMI..."
fi
