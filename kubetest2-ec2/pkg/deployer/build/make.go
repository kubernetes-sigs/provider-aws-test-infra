/*
Copyright 2023 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package build

import (
	"fmt"
	"runtime"

	"k8s.io/klog/v2"

	"sigs.k8s.io/kubetest2/pkg/exec"
	"sigs.k8s.io/provider-aws-test-infra/kubetest2-ec2/pkg/deployer/utils"
)

type MakeBuilder struct {
	RepoRoot        string
	TargetBuildArch string
}

var _ Builder = &MakeBuilder{}

const (
	target = "quick-release"
)

// Build builds kubernetes with the quick-release make target
func (m *MakeBuilder) Build() (string, error) {
	version, err := m.buildQuickRelease()
	if err != nil {
		return "", fmt.Errorf("failed to build quick release: %v", err)
	}
	if m.TargetBuildArch != runtime.GOOS+"/"+runtime.GOARCH {
		err = m.buildTestBinaries()
		if err != nil {
			return "", fmt.Errorf("failed to build test binaries: %v", err)
		}
	}
	return version, err
}

func (m *MakeBuilder) buildQuickRelease() (string, error) {
	version, err := utils.SourceVersion(m.RepoRoot)
	if err != nil {
		return "", fmt.Errorf("failed to get version: %v", err)
	}
	cmd := exec.Command("make", target,
		fmt.Sprintf("KUBE_BUILD_PLATFORMS=%s", m.TargetBuildArch))
	cmd.SetDir(m.RepoRoot)
	setSourceDateEpoch(m.RepoRoot, cmd)
	exec.InheritOutput(cmd)
	klog.Infof("running build %s using: KUBE_BUILD_PLATFORMS=%s", target, m.TargetBuildArch)
	if err = cmd.Run(); err != nil {
		return "", err
	}
	return version, nil
}

func (m *MakeBuilder) buildTestBinaries() error {
	cmd := exec.Command("make",
		fmt.Sprintf("WHAT=github.com/onsi/ginkgo/v2/ginkgo k8s.io/kubernetes/test/e2e/e2e.test k8s.io/kubernetes/cmd/kubectl"))
	cmd.SetDir(m.RepoRoot)
	setSourceDateEpoch(m.RepoRoot, cmd)
	exec.InheritOutput(cmd)
	klog.Infof("running build for test binaries")
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}
