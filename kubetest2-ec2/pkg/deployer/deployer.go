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

// Package deployer implements the kubetest2 ec2 deployer
package deployer

import (
	"flag"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/google/uuid"
	"github.com/octago/sflags/gen/gpflag"
	"github.com/spf13/pflag"

	"k8s.io/klog/v2"

	"sigs.k8s.io/kubetest2/pkg/artifacts"
	"sigs.k8s.io/kubetest2/pkg/exec"
	"sigs.k8s.io/kubetest2/pkg/types"

	"sigs.k8s.io/provider-aws-test-infra/kubetest2-ec2/pkg/deployer/build"
	"sigs.k8s.io/provider-aws-test-infra/kubetest2-ec2/pkg/deployer/options"
	"sigs.k8s.io/provider-aws-test-infra/kubetest2-ec2/pkg/deployer/remote"
)

// Name is the name of the deployer
const Name = "ec2"

const defaultAMD64InstanceType = "r5d.4xlarge"
const defaultARM64InstanceTYpe = "r7g.4xlarge"

var GitTag string

// New implements deployer.New for ec2
func New(opts types.Options) (types.Deployer, *pflag.FlagSet) {
	// create a deployer object and set fields that are not flag controlled
	user := remote.GetSSHUser()
	if user == "" {
		user = "ec2-user"
	}
	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		output, err := osexec.Command("go", "env", "GOPATH").CombinedOutput()
		if err == nil {
			gopath = strings.TrimSpace(string(output))
		}
	}
	k8sPath := filepath.Join(gopath, "src/k8s.io/kubernetes")
	info, err := os.Stat(k8sPath)
	if err != nil || !info.IsDir() {
		k8sPath = ""
	}
	d := &deployer{
		ClusterID:             "cid-" + uuid.New().String()[:8],
		ExternalCloudProvider: false,
		commonOptions:         opts,
		BuildOptions: &options.BuildOptions{
			CommonBuildOptions: &build.Options{
				Builder: &build.MakeBuilder{
					TargetBuildArch: "linux/amd64",
				},
				Stager: &build.S3Stager{
					TargetBuildArch: "linux/amd64",
				},
				TargetBuildArch: "linux/amd64",
			},
		},
		Ec2InstanceConnect: true,
		InstanceType:       defaultAMD64InstanceType,
		SSHUser:            user,
		SSHEnv:             "aws",
		Region:             "us-east-1",
		NumNodes:           2,
		logsDir:            filepath.Join(artifacts.BaseDir(), "logs"),
		InstanceProfile:    "provider-aws-test-instance-profile",
		RoleName:           "provider-aws-test-role",
		RepoRoot:           k8sPath,
	}
	// register flags and return
	return d, bindFlags(d)
}

// assert that New implements types.NewDeployer
var _ types.NewDeployer = New

type deployer struct {
	// generic parts
	commonOptions types.Options

	BuildOptions *options.BuildOptions

	kubectlPath string

	ClusterID      string `desc:"A unique name/id for the cluster."`
	KubeconfigPath string `flag:"kubeconfig" desc:"Absolute path to existing kubeconfig for cluster"`
	RepoRoot       string `desc:"The path to the root of the local kubernetes/kubernetes repo."`

	ExternalCloudProvider      bool   `desc:"Enable external AWS cloud provider"`
	ExternalCloudProviderImage string `desc:"repository:tag for the external cloud provider image"`

	Region             string `desc:"AWS region that the hosts live in (aws)"`
	UserDataFile       string `flag:"user-data-file" desc:"Path to user data to pass to control plane instances (aws)"`
	WorkerUserDataFile string `flag:"worker-user-data-file" desc:"Path to user data to pass to worker node instances (aws)"`
	KubeadmInitFile    string `desc:"custom kubeadm-init config file (aws)"`
	KubeadmJoinFile    string `desc:"custom kubeadm-join config file (aws)"`
	InstanceProfile    string `desc:"The name of the instance profile to assign to the node (aws)"`
	RoleName           string `desc:"The name of the role assign to the node (aws)"`
	Ec2InstanceConnect bool   `desc:"Use EC2 instance connect to generate a one time use key (aws)"`
	InstanceType       string `desc:"EC2 Instance type to use for test"`
	Image              string `flag:"image" desc:"Ubuntu image to use for test"`
	WorkerImage        string `flag:"worker-image" desc:"Worker image to use for test"`
	SSHUser            string `flag:"ssh-user" desc:"The SSH user to use for SSH access to instances"`
	SSHEnv             string `flag:"ssh-env" desc:"Use predefined ssh options for environment."`
	NumNodes           int    `flag:"num-nodes" desc:"Number of nodes in the cluster."`

	runner  *AWSRunner
	logsDir string
}

func (d *deployer) Down() error {
	if err := d.DumpClusterLogs(); err != nil {
		klog.Warningf("Dumping cluster logs at the start of Down() failed: %s", err)
	}
	for _, instance := range d.runner.instances {
		_, err := d.runner.ec2Service.TerminateInstances(&ec2.TerminateInstancesInput{
			InstanceIds: []*string{&instance.instanceID},
		})
		if err != nil {
			return fmt.Errorf("failed to delete instance %s : %w", instance.instanceID, err)
		}
		klog.Infof("deleted instance id: %s", instance.instanceID)
	}
	return nil
}

func (d *deployer) Kubeconfig() (string, error) {
	// noop deployer is specifically used with an existing cluster and KUBECONFIG
	if d.KubeconfigPath != "" {
		return d.KubeconfigPath, nil
	}
	if kconfig, ok := os.LookupEnv("KUBECONFIG"); ok {
		return kconfig, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".kube", "config"), nil
}

func (d *deployer) Version() string {
	return GitTag
}

func (d *deployer) waitForKubectlNodes() {
	if d.kubectlPath == "" {
		klog.Warningf("kubectl not found, cannot wait for all worker nodes to come up")
		return
	}
	if d.KubeconfigPath == "" {
		klog.Warningf("KUBECONFIG is not set, cannot wait for all worker nodes to come up")
		return
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
	for i := 0; i < 30; i++ {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.SetStderr(os.Stderr)
		lines, err := exec.OutputLines(cmd)
		if err != nil {
			klog.Errorf("unable to get nodes: %s", err)
			return
		}
		if len(lines) == len(d.runner.instances) {
			klog.Infof("found %d nodes in cluster %s: %v",
				len(lines),
				d.ClusterID,
				lines)
			break
		} else {
			klog.Infof("waiting for %d nodes in cluster %s, found %d",
				len(d.runner.instances),
				d.ClusterID,
				len(lines))
			time.Sleep(time.Second * 15)
		}
	}
}

func (d *deployer) waitForExternalProviderPods() {
	if d.kubectlPath == "" {
		klog.Warningf("kubectl not found, cannot wait for all worker nodes to come up")
		return
	}
	if d.KubeconfigPath == "" {
		klog.Warningf("KUBECONFIG is not set, cannot wait for all worker nodes to come up")
		return
	}
	args := []string{
		d.kubectlPath,
		"--kubeconfig",
		d.KubeconfigPath,
		"get",
		"pod",
		"-l",
		"k8s-app=aws-cloud-controller-manager",
		"-n",
		"kube-system",
		"-o",
		"name",
	}
	klog.Infof("Running kubectl command %v", args)
	controllerPodName := ""
	for i := 0; i < 30; i++ {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.SetStderr(os.Stderr)
		lines, err := exec.OutputLines(cmd)
		if err != nil {
			klog.Errorf("unable to get nodes: %s", err)
			return
		}
		if len(lines) >= 1 {
			klog.Infof("found %d pods in cluster %s: %v",
				len(lines),
				d.ClusterID,
				lines)
			controllerPodName = lines[0]
			break
		} else {
			klog.Infof("waiting for %d pods in cluster %s, found %d",
				len(d.runner.instances),
				d.ClusterID,
				len(lines))
			time.Sleep(time.Second * 15)
		}
	}

	args = []string{
		d.kubectlPath,
		"--kubeconfig",
		d.KubeconfigPath,
		"wait",
		controllerPodName,
		"-n",
		"kube-system",
		"--for=condition=Ready",
		"--timeout=300s",
	}
	klog.Infof("Running kubectl command %v", args)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.SetStderr(os.Stderr)
	_, err := exec.OutputLines(cmd)
	if err != nil {
		klog.Errorf("unable to wait for pod: %s", err)
		return
	}
}

// helper used to create & bind a flagset to the deployer
func bindFlags(d *deployer) *pflag.FlagSet {
	flags, err := gpflag.Parse(d)
	if err != nil {
		klog.Fatalf("unable to generate flags from deployer")
		return nil
	}

	klog.InitFlags(nil)
	flags.AddGoFlagSet(flag.CommandLine)

	return flags
}

// assert that deployer implements types.DeployerWithKubeconfig
var _ types.DeployerWithKubeconfig = &deployer{}
