package config

import "embed"

//go:embed *.sh *.yaml
var ConfigFS embed.FS
