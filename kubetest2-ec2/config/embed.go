package config

import "embed"

//go:embed configure.sh run-kubeadm.sh *.yaml
var ConfigFS embed.FS
