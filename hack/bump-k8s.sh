#!/bin/bash

KUBE_ROOT=$(dirname "${BASH_SOURCE}")/../../../k8s.io/kubernetes/

pushd "${KUBE_ROOT}" >/dev/null
git reset --hard HEAD && git clean -xdff
git fetch --all
git checkout master
popd >/dev/null

git reset --hard HEAD && git clean -xdff
go get k8s.io/kubernetes@HEAD
go mod tidy
go mod vendor

sed -i 's|k8s.io/kubernetes v.* =>|k8s.io/kubernetes v0.0.0 =>|' vendor/modules.txt
sed -i 's|k8s.io/kubernetes v.*|k8s.io/kubernetes v0.0.0|' go.mod

git status