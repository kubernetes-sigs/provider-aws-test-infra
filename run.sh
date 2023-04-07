export KUBE_BUILD_PLATFORMS="linux/amd64"

DELETE_INSTANCES=true \
  FOCUS=NodeConformance \
  IMAGE_CONFIG_FILE=aws-instance.yaml \
  IMAGE_CONFIG_DIR=config \
  TEST_ARGS='--container-runtime-endpoint=unix:///run/containerd/containerd.sock --container-runtime-process-name=/usr/bin/containerd --container-runtime-pid-file= --kubelet-flags="--cgroups-per-qos=true --cgroup-root=/ --runtime-cgroups=/system.slice/containerd.service" --extra-log="{\"name\": \"containerd.log\", \"journalctl\": [\"-u\", \"containerd*\"]}"' \
  SSH_USER=ec2-user \
  hack/make-rules/test-e2e-node.sh