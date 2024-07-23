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

package deployer

import (
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"sync"

	"github.com/google/uuid"

	"k8s.io/klog/v2"

	"sigs.k8s.io/kubetest2/pkg/exec"
	"sigs.k8s.io/kubetest2/pkg/fs"
	"sigs.k8s.io/provider-aws-test-infra/kubetest2-ec2/pkg/deployer/remote"
	"sigs.k8s.io/provider-aws-test-infra/kubetest2-ec2/pkg/deployer/utils"
)

type AWSImageConfig struct {
	Images map[string]AWSImage `json:"images"`
}

type AWSImage struct {
	AmiID           string   `json:"ami_id"`
	SSMPath         string   `json:"ssm_path,omitempty"`
	InstanceType    string   `json:"instance_type,omitempty"`
	UserData        string   `json:"user_data_file,omitempty"`
	InstanceProfile string   `json:"instance_profile,omitempty"`
	Tests           []string `json:"tests,omitempty"`
}

func (d *deployer) IsUp() (up bool, err error) {
	for _, instance := range d.runner.instances {
		instance2, err := d.runner.isAWSInstanceRunning(instance)
		if err != nil {
			return false, err
		}
		if instance2 == nil {
			return false, fmt.Errorf("instance2 %s not yet started", instance.instanceID)
		}
		klog.Infof("found instance2 id: %s", instance2.instanceID)
		if d.KubeconfigPath == "" {
			d.KubeconfigPath = downloadKubeConfig(instance2.instanceID, instance2.publicIP)
			klog.Infof("Updating $HOME/.kube/config")
			home, _ := os.UserHomeDir()
			_ = fs.CopyFile(d.KubeconfigPath, filepath.Join(home, ".kube", "config"))
		}
		break
	}
	args := []string{
		d.kubectlPath,
		"--kubeconfig",
		d.KubeconfigPath,
		"get",
		"nodes",
		"-o=name",
	}
	klog.Infof("Running kubectl command %v", args)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.SetStderr(os.Stderr)
	lines, err := exec.OutputLines(cmd)
	if err != nil {
		return false, fmt.Errorf("is up failed to get nodes: %s", err)
	}

	return len(lines) > 0, nil
}

// verifyKubectl checks if kubectl exists in kubetest2 artifacts or PATH
// returns the path to the binary, error if it doesn't exist
// kubectl detection using legacy verify-get-kube-binaries is unreliable
// https://github.com/kubernetes/kubernetes/blob/b10d82b93bad7a4e39b9d3f5c5e81defa3af68f0/cluster/kubectl.sh#L25-L26
func (d *deployer) verifyKubectl() (string, error) {
	klog.Infof("checking locally built kubectl ...")
	localKubectl := filepath.Join(d.commonOptions.RunDir(), "kubectl")
	if _, err := os.Stat(localKubectl); err == nil {
		return localKubectl, nil
	}
	klog.Infof("could not find locally built kubectl, checking existence of kubectl in $PATH ...")
	kubectlPath, err := osexec.LookPath("kubectl")
	if err != nil {
		return "", fmt.Errorf("could not find kubectl in $PATH, please ensure your environment has the kubectl binary")
	}
	return kubectlPath, nil
}

func (d *deployer) Up() error {
	klog.Info("EC2 deployer starting Up()")

	path, err := d.verifyKubectl()
	if err != nil {
		return err
	}
	d.kubectlPath = path

	runner := d.NewAWSRunner()
	err = runner.Validate()
	if err != nil {
		return err
	}

	var wg sync.WaitGroup
	fatalErrors := make(chan error)
	wgDone := make(chan bool)

	for _, image := range runner.internalAWSImages {
		instance, err := runner.createAWSInstance(image)
		if instance != nil {
			runner.instances = append(runner.instances, instance)
		}
		if err != nil {
			klog.Errorf("error starting instance for image %s : %s", image.AmiID, err)
			if err2 := d.DumpClusterLogs(); err2 != nil {
				klog.Warningf("Dumping cluster logs at the when Up() failed: %s", err2)
			}
			return err
		}
		if runner.controlPlaneIP == "" {
			runner.controlPlaneIP = instance.privateIP
		}
		klog.Infof("started instance id: %s", instance.instanceID)

		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := runner.isAWSInstanceRunning(instance)
			if err != nil {
				klog.Errorf("error checking instance is running %s : %s", instance.instanceID, err)
				if err2 := d.DumpClusterLogs(); err2 != nil {
					klog.Warningf("Dumping cluster logs at the when Up() failed: %s", err2)
				}
				fatalErrors <- err
			}
			klog.Infof("instance is running: %s", instance.instanceID)
		}()
	}

	go func() {
		wg.Wait()
		close(wgDone)
	}()

	select {
	case <-wgDone:
		break
	case err := <-fatalErrors:
		close(fatalErrors)
		return err
	}

	d.waitForKubectlNodes()
	d.waitForKubectlNodesToBeReady()
	if d.ExternalCloudProvider {
		d.waitForExternalProviderPods()
	}
	return nil
}

func (d *deployer) NewAWSRunner() *AWSRunner {
	d.runner = &AWSRunner{
		deployer:           d,
		instanceNamePrefix: "tmp-e2e-" + uuid.New().String()[:8],
		token:              utils.RandomFixedLengthString(6) + "." + utils.RandomFixedLengthString(16),
		certificateKey:     utils.RandomHexEncodedBytes(32),
	}
	return d.runner
}

func downloadKubeConfig(instanceID string, publicIp string) string {
	output, err := remote.SSH(instanceID, "cat /etc/kubernetes/admin.conf")
	if err != nil {
		klog.Fatalf("error downloading KUBECONFIG file: %v", err)
	}
	// write our KUBECONFIG to disk and register it
	f, err := os.CreateTemp("", ".kubeconfig-*")
	if err != nil {
		klog.Fatalf("creating KUBECONFIG file, %w", err)
	}
	kubeconfigFile := f.Name()
	if err = os.Chmod(kubeconfigFile, 0400); err != nil {
		klog.Fatalf("chmod'ing KUBECONFIG file, %w", err)
	}

	var re = regexp.MustCompile(`server: https://(.*):6443`)
	output = re.ReplaceAllString(output, "server: https://"+publicIp+":6443")

	if _, err = f.Write([]byte(output)); err != nil {
		klog.Fatalf("writing KUBECONFIG file, %w", err)
	}
	klog.Infof("KUBECONFIG=%v", f.Name())
	return f.Name()
}
