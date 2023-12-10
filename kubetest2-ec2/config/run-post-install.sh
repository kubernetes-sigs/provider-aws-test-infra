#!/bin/bash
set -xeu
if [[ "${KUBEADM_CONTROL_PLANE}" == true ]]; then
  CNI_VERSION=$(curl -s https://api.github.com/repos/aws/amazon-vpc-cni-k8s/releases/latest | jq -r ".name")
  kubectl --kubeconfig /etc/kubernetes/admin.conf create -f https://raw.githubusercontent.com/aws/amazon-vpc-cni-k8s/${CNI_VERSION}/config/master/aws-k8s-cni.yaml
  kubectl --kubeconfig /etc/kubernetes/admin.conf set env daemonset aws-node -n kube-system ENABLE_PREFIX_DELEGATION=true
  kubectl --kubeconfig /etc/kubernetes/admin.conf set env daemonset aws-node -n kube-system MINIMUM_IP_TARGET=80
  kubectl --kubeconfig /etc/kubernetes/admin.conf set env daemonset aws-node -n kube-system WARM_IP_TARGET=10
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
    mkdir cloud-provider-aws
    for f in "${files[@]}"
    do
      curl -sSLo ./cloud-provider-aws/${f} --fail --retry 5 "https://raw.githubusercontent.com/kubernetes/cloud-provider-aws/master/examples/existing-cluster/base/${f}"
    done
    if [[ "{{EXTERNAL_CLOUD_PROVIDER_IMAGE}}" != "" ]]; then
      sed -i "s|registry.k8s.io/provider-aws/cloud-controller-manager.*$|{{EXTERNAL_CLOUD_PROVIDER_IMAGE}}|" ./cloud-provider-aws/aws-cloud-controller-manager-daemonset.yaml
    fi
    kubectl --kubeconfig /etc/kubernetes/admin.conf apply -k ./cloud-provider-aws/
    # Install the AWS EBS CSI driver
    kubectl --kubeconfig /etc/kubernetes/admin.conf apply -k "github.com/kubernetes-sigs/aws-ebs-csi-driver/deploy/kubernetes/overlays/stable/?ref=release-1.25"
    kubectl --kubeconfig /etc/kubernetes/admin.conf wait --for=condition=Available --timeout=2m -n kube-system deployments ebs-csi-controller
  fi
  # shellcheck disable=SC2050
  if [[ "{{EXTERNAL_LOAD_BALANCER}}" == "true" ]]; then
    kubectl --kubeconfig /etc/kubernetes/admin.conf apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.13.2/cert-manager.yaml
    kubectl --kubeconfig /etc/kubernetes/admin.conf wait --for=condition=Available --timeout=2m -n cert-manager --all deployments
    kubectl --kubeconfig /etc/kubernetes/admin.conf apply -f https://github.com/kubernetes-sigs/aws-load-balancer-controller/releases/download/v2.6.2/v2_6_2_full.yaml
    kubectl --kubeconfig /etc/kubernetes/admin.conf wait --for=condition=Available --timeout=2m -n kube-system deployments aws-load-balancer-controller
  fi
fi
