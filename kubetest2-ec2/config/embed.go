package config

import "embed"

//go:embed configure.sh run-kubeadm.sh run-post-install.sh *.yaml
var ConfigFS embed.FS
