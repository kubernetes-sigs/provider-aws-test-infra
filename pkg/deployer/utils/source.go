package utils

import (
	"fmt"
	"sigs.k8s.io/kubetest2/pkg/exec"
	"strings"
)

// SourceVersion the kubernetes git version based on hack/print-workspace-status.sh
// the raw version is also returned
func SourceVersion(kubeRoot string) (string, error) {
	// get the version output
	cmd := exec.Command("sh", "-c", "hack/print-workspace-status.sh")
	cmd.SetDir(kubeRoot)
	output, err := exec.CombinedOutputLines(cmd)
	if err != nil {
		return "", err
	}

	// parse it, and populate it into _output/git_version
	version := ""
	for _, line := range output {
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			return "", fmt.Errorf("could not parse kubernetes version: %q", strings.Join(output, "\n"))
		}
		if parts[0] == "gitVersion" {
			version = parts[1]
			return version, nil
		}
	}
	if version == "" {
		return "", fmt.Errorf("could not obtain kubernetes version: %q", strings.Join(output, "\n"))

	}
	return "", fmt.Errorf("could not find kubernetes version in output: %q", strings.Join(output, "\n"))
}
