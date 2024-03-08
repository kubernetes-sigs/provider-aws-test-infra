#!/bin/bash

set -xeuo pipefail

SCRIPTDIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
KUBE_ROOT=${SCRIPTDIR}/../../../k8s.io/kubernetes/

# shellcheck disable=SC2164
pushd "${KUBE_ROOT}" >/dev/null
git reset --hard HEAD && git clean -xdff
git fetch --all
git checkout master
# shellcheck disable=SC2164
popd >/dev/null

git reset --hard HEAD && git clean -xdff

export GOPROXY=direct
go get k8s.io/kubernetes@HEAD
go mod tidy
go mod vendor

sed -i 's|k8s.io/kubernetes v.* =>|k8s.io/kubernetes v0.0.0 =>|' vendor/modules.txt
sed -i 's|k8s.io/kubernetes v.*|k8s.io/kubernetes v0.0.0|' go.mod

git add -f .
git status
