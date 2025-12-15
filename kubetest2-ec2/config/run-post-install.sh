#!/bin/bash
set -xeu
if [[ "${KUBEADM_CONTROL_PLANE}" == true ]]; then
  # shellcheck disable=SC2050
  if [[ "{{EXTERNAL_CLOUD_PROVIDER}}" == "external" ]]; then
    #CNI_VERSION=$(curl -s https://api.github.com/repos/aws/amazon-vpc-cni-k8s/releases/latest | jq -r ".name")
    CNI_VERSION=v1.20.4
    kubectl --kubeconfig /etc/kubernetes/admin.conf create -f https://raw.githubusercontent.com/aws/amazon-vpc-cni-k8s/${CNI_VERSION}/config/master/aws-k8s-cni.yaml
    kubectl --kubeconfig /etc/kubernetes/admin.conf set env daemonset aws-node -n kube-system ENABLE_PREFIX_DELEGATION=true
    kubectl --kubeconfig /etc/kubernetes/admin.conf set env daemonset aws-node -n kube-system MINIMUM_IP_TARGET=160
    kubectl --kubeconfig /etc/kubernetes/admin.conf set env daemonset aws-node -n kube-system WARM_IP_TARGET=20
    kubectl --kubeconfig /etc/kubernetes/admin.conf set env daemonset aws-node -n kube-system AWS_VPC_K8S_CNI_EXCLUDE_SNAT_CIDRS=10.0.0.0/8
  else
    echo "Installing Cilium CNI..."
    CILIUM_CLI_VERSION=$(curl -s https://raw.githubusercontent.com/cilium/cilium-cli/main/stable.txt)
    CLI_ARCH=amd64
    if [ "$(uname -m)" = "aarch64" ]; then CLI_ARCH=arm64; fi
    curl -L --fail --remote-name-all https://github.com/cilium/cilium-cli/releases/download/${CILIUM_CLI_VERSION}/cilium-linux-${CLI_ARCH}.tar.gz{,.sha256sum}
    sha256sum --check cilium-linux-${CLI_ARCH}.tar.gz.sha256sum
    tar xzvfC cilium-linux-${CLI_ARCH}.tar.gz /usr/local/bin
    rm cilium-linux-${CLI_ARCH}.tar.gz{,.sha256sum}
    HOME=/root cilium install --version 1.18.4 \
      --set cni.chainingMode=portmap \
      --set kubeProxyReplacement=false \
      --set socketLB.enabled=false \
      --set sessionAffinity=true \
      --set externalIPs.enabled=true \
      --set nodePort.enabled=true \
      --set hostPort.enabled=false \
      --set cluster.name=kubernetes \
      --set ipam.mode=kubernetes \
      --kubeconfig /etc/kubernetes/admin.conf
    HOME=/root cilium status --wait --kubeconfig /etc/kubernetes/admin.conf
    echo "Cilium CNI installed successfully"
  fi
  # shellcheck disable=SC2050
  if [[ "{{EXTERNAL_CLOUD_PROVIDER}}" == "external" ]]; then
    files=(
      "kustomization.yaml"
      "apiserver-authentication-reader-role-binding.yaml"
      "aws-cloud-controller-manager-daemonset.yaml"
      "cluster-role-binding.yaml"
      "cluster-role.yaml"
      "service-account.yaml"
    )
    mkdir -p cloud-provider-aws
    for f in "${files[@]}"
    do
      curl -sSLo ./cloud-provider-aws/${f} --fail --retry 5 "https://raw.githubusercontent.com/kubernetes/cloud-provider-aws/master/examples/existing-cluster/base/${f}"
    done
    if [[ "{{EXTERNAL_CLOUD_PROVIDER_IMAGE}}" != "" ]]; then
      sed -i "s|registry.k8s.io/provider-aws/cloud-controller-manager.*$|{{EXTERNAL_CLOUD_PROVIDER_IMAGE}}|" ./cloud-provider-aws/aws-cloud-controller-manager-daemonset.yaml
    fi
    kubectl --kubeconfig /etc/kubernetes/admin.conf apply -k ./cloud-provider-aws/
    # Install the AWS EBS CSI driver
    kubectl --kubeconfig /etc/kubernetes/admin.conf apply -k "github.com/kubernetes-sigs/aws-ebs-csi-driver/deploy/kubernetes/overlays/stable/?ref=release-1.32"
    kubectl --kubeconfig /etc/kubernetes/admin.conf wait --for=condition=Available --timeout=2m -n kube-system deployments ebs-csi-controller
  fi
  # shellcheck disable=SC2050
  if [[ "{{EXTERNAL_LOAD_BALANCER}}" == "true" ]]; then
    kubectl --kubeconfig /etc/kubernetes/admin.conf apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.15.1/cert-manager.yaml
    kubectl --kubeconfig /etc/kubernetes/admin.conf wait --for=condition=Available --timeout=2m -n cert-manager --all deployments
    kubectl --kubeconfig /etc/kubernetes/admin.conf apply -f https://github.com/kubernetes-sigs/aws-load-balancer-controller/releases/download/v2.8.1/v2_8_1_full.yaml
    kubectl --kubeconfig /etc/kubernetes/admin.conf wait --for=condition=Available --timeout=2m -n kube-system deployments aws-load-balancer-controller
  fi
  # shellcheck disable=SC2050
  if [[ "{{ENABLE_NVIDIA_DEVICE_PLUGIN}}" == "true" ]]; then
    kubectl --kubeconfig /etc/kubernetes/admin.conf apply -f https://raw.githubusercontent.com/NVIDIA/k8s-device-plugin/v0.16.2/deployments/static/nvidia-device-plugin.yml
    kubectl --kubeconfig /etc/kubernetes/admin.conf rollout status daemonset nvidia-device-plugin-daemonset -n kube-system --timeout=2m
  fi
  kubectl --kubeconfig /etc/kubernetes/admin.conf wait --for=condition=Ready pod -l k8s-app=kube-dns -n kube-system --timeout=2m
fi
