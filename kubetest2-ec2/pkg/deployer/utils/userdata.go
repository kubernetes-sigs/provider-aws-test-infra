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

package utils

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"sigs.k8s.io/provider-aws-test-infra/kubetest2-ec2/config"
)

func gzipAndBase64Encode(fileBytes []byte) (string, error) {
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	if _, err := gz.Write(fileBytes); err != nil {
		return "", err
	}
	if err := gz.Flush(); err != nil {
		return "", err
	}
	if err := gz.Close(); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buffer.Bytes()), nil
}

func FetchConfigureScript(userDataFile string) (string, error) {
	var scriptBytes []byte
	var err error
	if userDataFile != "" {
		scriptFile := filepath.Dir(userDataFile) + "/" + "configure.sh"
		if _, err = os.Stat(scriptFile); errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		scriptBytes, err = os.ReadFile(scriptFile)
		if err != nil {
			return "", fmt.Errorf("reading configure script file %q, %w", scriptFile, err)
		}
	} else {
		scriptBytes, err = config.ConfigFS.ReadFile("configure.sh")
		if err != nil {
			return "", fmt.Errorf("error reading configure script file: %w", err)
		}
	}
	return gzipAndBase64Encode(scriptBytes)
}

func FetchKubeadmInitYaml(kubeadmInitFile string, replace func(string) string) (string, error) {
	var yamlBytes []byte
	var err error
	if kubeadmInitFile != "" {
		yamlBytes, err = os.ReadFile(kubeadmInitFile)
		if err != nil {
			return "", fmt.Errorf("reading kubeadm-init.yaml file %q, %w", kubeadmInitFile, err)
		}
	} else {
		yamlBytes, err = config.ConfigFS.ReadFile("kubeadm-init.yaml")
		if err != nil {
			return "", fmt.Errorf("error reading kubeadm-init.yaml: %w", err)
		}
	}
	yamlBytes = []byte(replace(string(yamlBytes)))
	yamlString, err := gzipAndBase64Encode(yamlBytes)
	if err != nil {
		return "", fmt.Errorf("error reading kubeadm-init.yaml: %w", err)
	}
	return yamlString, nil
}

func FetchKubeadmJoinYaml(kubeadmJoinFile string, replace func(string) string) (string, error) {
	var yamlBytes []byte
	var err error
	if kubeadmJoinFile != "" {
		yamlBytes, err = os.ReadFile(kubeadmJoinFile)
		if err != nil {
			return "", fmt.Errorf("reading kubeadm-join.yaml file %q, %w", kubeadmJoinFile, err)
		}
	} else {
		yamlBytes, err = config.ConfigFS.ReadFile("kubeadm-join.yaml")
		if err != nil {
			return "", fmt.Errorf("error reading kubeadm-join.yaml: %w", err)
		}
	}
	yamlBytes = []byte(replace(string(yamlBytes)))
	yamlString, err := gzipAndBase64Encode(yamlBytes)
	if err != nil {
		return "", fmt.Errorf("error reading kubeadm-join.yaml: %w", err)
	}
	return yamlString, nil
}

func FetchRunKubeadmSH(replace func(string) string) (string, error) {
	var scriptBytes []byte
	var err error
	scriptBytes, err = config.ConfigFS.ReadFile("run-kubeadm.sh")
	if err != nil {
		return "", fmt.Errorf("error reading run-kubeadm.sh: %w", err)
	}
	scriptBytes = []byte(replace(string(scriptBytes)))
	scriptString, err := gzipAndBase64Encode(scriptBytes)
	if err != nil {
		return "", fmt.Errorf("error reading run-kubeadm.sh: %w", err)
	}
	return scriptString, nil
}
