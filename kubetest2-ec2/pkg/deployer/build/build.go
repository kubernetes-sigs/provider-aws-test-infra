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

// Package build implements a common system for building kubernetes for deployers to use.
package build

import (
	"fmt"
	"k8s.io/klog/v2"
	"os"
	"path/filepath"
	"sigs.k8s.io/kubetest2/pkg/exec"
	"sigs.k8s.io/kubetest2/pkg/fs"
)

type Builder interface {
	// Build determines how kubernetes artifacts are built from sources or existing artifacts
	// and returns the version being built
	Build() (string, error)
}

type NoopBuilder struct{}

var _ Builder = &NoopBuilder{}

func (n *NoopBuilder) Build() (string, error) {
	return "", nil
}

var (
	CommonTestBinaries = []string{
		"kubectl",
		"e2e.test",
		"ginkgo",
	}
)

// StoreCommonBinaries will best effort try to store commonly built binaries
// to the output directory
func StoreCommonBinaries(kuberoot string, outroot string, targetBuildArch string) {
	const localOutput = "_output/local"
	root := filepath.Join(kuberoot, localOutput, "bin", targetBuildArch)
	for _, binary := range CommonTestBinaries {
		source := filepath.Join(root, binary)
		dest := filepath.Join(outroot, binary)
		if _, err := os.Stat(source); err == nil {
			klog.Infof("copying %s to %s ...", source, dest)
			if err := fs.CopyFile(source, dest); err != nil {
				klog.Warningf("failed to copy %s to %s: %v", source, dest, err)
			}
		} else {
			klog.Warningf("could not find %s: %v", source, err)
		}
	}
}

// setSourceDateEpoch sets the SOURCE_DATE_EPOCH env to the commit timestamp of the latest commit in the
// kubernetes repository, specified under kubeRoot, for reproducible builds
// https://github.com/kubernetes/kubernetes/blob/7eae33cb0e1ead51c80ad517bc670113d77fa28d/build/README.md#reproducibility
func setSourceDateEpoch(kubeRoot string, cmd exec.Cmd) {
	if os.Getenv("SOURCE_DATE_EPOCH") != "" {
		return
	}
	gitCmd := exec.Command("git", "log", "-1", "--pretty=%ct")
	gitCmd.SetDir(kubeRoot)
	if output, err := exec.CombinedOutputLines(gitCmd); err == nil {
		env := append(os.Environ(), fmt.Sprintf("SOURCE_DATE_EPOCH=%s", output[0]))
		cmd.SetEnv(env...)
	} else {
		klog.Warningf("failed to compute SOURCE_DATE_EPOCH from kubernetes repository: %v", err)
	}
}
