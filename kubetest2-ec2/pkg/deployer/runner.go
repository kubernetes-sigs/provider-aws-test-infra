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
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	awsv2 "github.com/aws/aws-sdk-go-v2/aws"
	configv2 "github.com/aws/aws-sdk-go-v2/config"
	s3managerv2 "github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	ec2v2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2typesv2 "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	ec2instanceconnectv2 "github.com/aws/aws-sdk-go-v2/service/ec2instanceconnect"
	iamv2 "github.com/aws/aws-sdk-go-v2/service/iam"
	s3v2 "github.com/aws/aws-sdk-go-v2/service/s3"
	ssmv2 "github.com/aws/aws-sdk-go-v2/service/ssm"

	"golang.org/x/crypto/ssh"
	"k8s.io/klog/v2"

	"sigs.k8s.io/kubetest2/pkg/fs"
	"sigs.k8s.io/provider-aws-test-infra/kubetest2-ec2/config"
	"sigs.k8s.io/provider-aws-test-infra/kubetest2-ec2/pkg/deployer/remote"
	"sigs.k8s.io/provider-aws-test-infra/kubetest2-ec2/pkg/deployer/utils"
)

type AWSRunner struct {
	deployer           *deployer
	ec2Service         *ec2v2.Client
	ec2icService       *ec2instanceconnectv2.Client
	ssmService         *ssmv2.Client
	iamService         *iamv2.Client
	s3Service          *s3v2.Client
	instanceNamePrefix string
	internalAWSImages  []utils.InternalAWSImage
	instances          []*awsInstance
	token              string
	certificateKey     string
	controlPlaneIP     string
	subnetID           string
}

type awsInstance struct {
	instance         *ec2typesv2.Instance
	instanceID       string
	sshKey           *utils.TemporarySSHKey
	publicIP         string
	privateIP        string
	sshPublicKeyFile string
}

var operatingSystems = []string{
	"ubuntu",
	"al2023",
	"al2",
}

func (a *AWSRunner) Validate() error {
	_, err := a.InitializeServices()
	if err != nil {
		return fmt.Errorf("unable to initialize AWS services : %w", err)
	}

	bucket := a.deployer.BuildOptions.CommonBuildOptions.StageLocation
	if bucket == "" {
		return fmt.Errorf("please specify --stage with the s3 bucket")
	}
	if !strings.Contains(bucket, "://") {
		_, err = a.s3Service.HeadBucket(context.TODO(),
			&s3v2.HeadBucketInput{Bucket: awsv2.String(bucket)})
		if err != nil {
			return fmt.Errorf("unable to find bucket %q, %v", bucket, err)
		}
	}

	if a.deployer.Image == "" || slices.Contains(operatingSystems, a.deployer.Image) {
		arch := strings.Split(a.deployer.BuildOptions.CommonBuildOptions.TargetBuildArch, "/")[1]

		path := ""
		switch a.deployer.Image {
		case "al2023":
			if arch == "amd64" {
				path = "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-x86_64"
			} else {
				path = "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-" + arch
			}
			if a.deployer.UserDataFile == "" {
				a.deployer.UserDataFile = "al2023.sh"
			}
		case "al2":
			if arch == "amd64" {
				path = "/aws/service/ami-amazon-linux-latest/amzn2-ami-hvm-x86_64-gp2"
			} else {
				path = "/aws/service/ami-amazon-linux-latest/amzn2-ami-hvm-" + arch + "-gp2"
			}
			if a.deployer.UserDataFile == "" {
				// this is intentional, same file works on both al2 and al2023 and the al2.sh is
				// symlinked to al2023.sh
				a.deployer.UserDataFile = "al2023.sh"
			}
		case "ubuntu", "":
			path = "/aws/service/canonical/ubuntu/server/24.04/stable/20250305/" + arch + "/hvm/ebs-gp3/ami-id"
			if a.deployer.UserDataFile == "" {
				a.deployer.UserDataFile = "ubuntu2404.yaml"
			}
		default:
			return fmt.Errorf("unrecognized parameter --image : %s", a.deployer.Image)
		}
		klog.Infof("looking up latest image in SSM:")
		klog.Infof("%s", path)

		id, err := utils.GetSSMImage(a.ssmService, path)
		if err == nil {
			klog.Infof("using image id from ssm %s", id)
			a.deployer.Image = id
		} else {
			return fmt.Errorf("error looking up ssm : %w", err)
		}

		// Looks like we need an arm64 image and the default instance type is amd64, so
		// pick an equivalent image to t3a.medium which is t4g.medium.
		if a.deployer.InstanceType == defaultAMD64InstanceType && arch == "arm64" {
			a.deployer.InstanceType = defaultARM64InstanceTYpe
		}
		if a.deployer.WorkerInstanceType == defaultAMD64InstanceType && arch == "arm64" {
			a.deployer.WorkerInstanceType = defaultARM64InstanceTYpe
		}
	}

	if len(a.deployer.Image) == 0 {
		return fmt.Errorf("must specify an Ubuntu AMI using --image")
	}

	if !strings.HasPrefix(a.deployer.Image, "ami-") {
		return fmt.Errorf("invalid AMI id format for %q", a.deployer.Image)
	}

	if a.deployer.WorkerImage == "" || slices.Contains(operatingSystems, a.deployer.WorkerImage) {
		arch := strings.Split(a.deployer.BuildOptions.CommonBuildOptions.TargetBuildArch, "/")[1]

		path := ""
		switch a.deployer.WorkerImage {
		case "al2023":
			if arch == "amd64" {
				path = "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-x86_64"
			} else {
				path = "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-" + arch
			}
			if a.deployer.WorkerUserDataFile == "" {
				a.deployer.WorkerUserDataFile = "al2023.sh"
			}
		case "al2":
			if arch == "amd64" {
				path = "/aws/service/ami-amazon-linux-latest/amzn2-ami-hvm-x86_64-gp2"
			} else {
				path = "/aws/service/ami-amazon-linux-latest/amzn2-ami-hvm-" + arch + "-gp2"
			}
			if a.deployer.WorkerUserDataFile == "" {
				// this is intentional, same file works on both al2 and al2023 and the al2.sh is
				// symlinked to al2023.sh
				a.deployer.WorkerUserDataFile = "al2023.sh"
			}
		case "ubuntu", "":
			path = "/aws/service/canonical/ubuntu/server/24.04/stable/20250305/" + arch + "/hvm/ebs-gp3/ami-id"
			if a.deployer.WorkerUserDataFile == "" {
				a.deployer.WorkerUserDataFile = "ubuntu2404.yaml"
			}
		default:
			return fmt.Errorf("unrecognized parameter --worker-image : %s", a.deployer.WorkerImage)
		}
		klog.Infof("looking up latest image in SSM:")
		klog.Infof("%s", path)
		id, err := utils.GetSSMImage(a.ssmService, path)
		if err == nil {
			klog.Infof("using image id from ssm %s", id)
			a.deployer.WorkerImage = id
		} else {
			return fmt.Errorf("error looking up ssm : %w", err)
		}

		// Looks like we need an arm64 image and the default instance type is amd64, so
		// pick an equivalent image to t3a.medium which is t4g.medium.
		if a.deployer.InstanceType == defaultAMD64InstanceType && arch == "arm64" {
			a.deployer.InstanceType = defaultARM64InstanceTYpe
		}
		if a.deployer.WorkerInstanceType == defaultAMD64InstanceType && arch == "arm64" {
			a.deployer.WorkerInstanceType = defaultARM64InstanceTYpe
		}
	}

	if len(a.deployer.WorkerImage) == 0 {
		return fmt.Errorf("must specify an AMI using --worker-image")
	}

	if !strings.HasPrefix(a.deployer.WorkerImage, "ami-") {
		return fmt.Errorf("invalid AMI id format for %q", a.deployer.WorkerImage)
	}

	if err = a.ensureInstanceProfileAndRole(); err != nil {
		return fmt.Errorf("while creating instance profile / roles : %v", err)
	}

	a.internalAWSImages, err = a.prepareAWSImages()
	if err != nil {
		return fmt.Errorf("while preparing AWS images: %v", err)
	}
	return nil
}

func (a *AWSRunner) isAWSInstanceRunning(testInstance *awsInstance) (*awsInstance, error) {
	instanceRunning := false
	createdSSHKey := false
	klog.Infof("waiting for %s to start (5 mins)", testInstance.instanceID)

	err := ec2v2.NewInstanceRunningWaiter(a.ec2Service).Wait(context.TODO(), &ec2v2.DescribeInstancesInput{
		InstanceIds: []string{testInstance.instanceID},
	}, 5*time.Minute)

	if err != nil {
		return testInstance, fmt.Errorf("instance %s did not start running", testInstance.instanceID)
	}
	for i := 0; i < 30 && !instanceRunning; i++ {
		if i > 0 {
			time.Sleep(time.Second * 15)
		}

		op, err := a.ec2Service.DescribeInstances(context.TODO(),
			&ec2v2.DescribeInstancesInput{
				InstanceIds: []string{testInstance.instanceID},
			})
		if err != nil {
			continue
		}
		instance := op.Reservations[0].Instances[0]
		if instance.State.Name != ec2typesv2.InstanceStateNameRunning {
			continue
		}
		if len(instance.NetworkInterfaces) == 0 {
			klog.Infof("instance %s does not have network interfaces yet", testInstance.instanceID)
			continue
		}
		sourceDestCheck := instance.NetworkInterfaces[0].SourceDestCheck
		if sourceDestCheck != nil && *sourceDestCheck == true {
			networkInterfaceID := instance.NetworkInterfaces[0].NetworkInterfaceId
			modifyInput := &ec2v2.ModifyNetworkInterfaceAttributeInput{
				NetworkInterfaceId: networkInterfaceID,
				SourceDestCheck:    &ec2typesv2.AttributeBooleanValue{Value: awsv2.Bool(false)},
			}
			_, err = a.ec2Service.ModifyNetworkInterfaceAttribute(context.TODO(), modifyInput)
			if err != nil {
				klog.Infof("unable to set SourceDestCheck on instance %s", testInstance.instanceID)
			}
		}
		testInstance.publicIP = *instance.PublicIpAddress
		testInstance.privateIP = *instance.PrivateIpAddress

		// generate a temporary SSH key and send it to the node via instance-connect
		if a.deployer.Ec2InstanceConnect && !createdSSHKey {
			klog.Info("instance-connect flag is set, using ec2 instance connect to configure a temporary SSH key")
			err = a.assignNewSSHKey(testInstance)
			if err != nil {
				klog.Infof("instance connect err = %s", err)
				continue
			}
			createdSSHKey = true
		}

		klog.Infof("registering %s/%s", testInstance.instanceID, testInstance.publicIP)
		remote.AddHostnameIP(testInstance.instanceID, testInstance.publicIP)

		// ensure that containerd or CRIO is running
		var output string
		output, err = remote.SSH(testInstance.instanceID, "sh", "-c", "systemctl list-units  --type=service  --state=running | grep -e containerd -e crio")
		if err != nil {
			err = fmt.Errorf("instance %s not running containerd/crio daemon - Command failed: %s", testInstance.instanceID, output)
			continue
		}
		if !strings.Contains(output, "containerd.service") &&
			!strings.Contains(output, "crio.service") {
			err = fmt.Errorf("instance %s not yet running containerd/crio daemon: %s", testInstance.instanceID, output)
			continue
		}

		output, err = remote.SSH(testInstance.instanceID, "sh", "-c", "systemctl status cloud-init.service")
		if err != nil {
			err = fmt.Errorf("checking instance %s is running cloud-init - Command failed: %s", testInstance.instanceID, output)
			continue
		}
		if !strings.Contains(output, "exited") {
			err = fmt.Errorf("instance %s is still running cloud-init daemon: %s", testInstance.instanceID, output)
			continue
		}

		if a.controlPlaneIP == *testInstance.instance.PrivateIpAddress {
			output, err = remote.SSH(testInstance.instanceID, "kubectl --kubeconfig /etc/kubernetes/admin.conf version")
			if err != nil {
				err = fmt.Errorf("checking instance %s is api server running - Command failed: %s", testInstance.instanceID, output)
				continue
			}
			output, err = remote.SSH(testInstance.instanceID, "kubectl --kubeconfig /etc/kubernetes/admin.conf get nodes -o name")
			if err != nil {
				err = fmt.Errorf("checking instance %s is node present - Command failed: %s", testInstance.instanceID, output)
				continue
			}
			if !strings.Contains(output, "node/") {
				err = fmt.Errorf("instance %s does not yet have a node: %s", testInstance.instanceID, output)
				continue
			}
		}

		instanceRunning = true
	}

	if !instanceRunning {
		return testInstance, fmt.Errorf("instance %s is not running", testInstance.instanceID)
	} else {
		if a.controlPlaneIP == *testInstance.instance.PrivateIpAddress {
			if a.deployer.KubeconfigPath == "" {
				a.deployer.KubeconfigPath = downloadKubeConfig(testInstance.instanceID, testInstance.publicIP)
				klog.Infof("Updating $HOME/.kube/config")
				home, _ := os.UserHomeDir()
				_ = fs.CopyFile(a.deployer.KubeconfigPath, filepath.Join(home, ".kube", "config"))
			}
		}
	}
	klog.Infof("instance %s is running", testInstance.instanceID)
	return testInstance, nil
}

func (a *AWSRunner) InitializeServices() (*awsv2.Config, error) {

	cfg, err := configv2.LoadDefaultConfig(context.TODO(),
		configv2.WithRegion(a.deployer.Region),
	)
	if err != nil {
		return nil, fmt.Errorf("unable to load default config, %w", err)
	}

	a.ec2Service = ec2v2.NewFromConfig(cfg)
	a.ec2icService = ec2instanceconnectv2.NewFromConfig(cfg)
	a.ssmService = ssmv2.NewFromConfig(cfg)
	a.iamService = iamv2.NewFromConfig(cfg)
	a.s3Service = s3v2.NewFromConfig(cfg)
	a.deployer.BuildOptions.CommonBuildOptions.S3Service = a.s3Service
	a.deployer.BuildOptions.CommonBuildOptions.S3Uploader = s3managerv2.NewUploader(a.s3Service, func(u *s3managerv2.Uploader) {
		u.PartSize = 10 * 1024 * 1024 // 10 MB
		u.Concurrency = 10
	})
	return &cfg, nil
}

func (a *AWSRunner) ensureInstanceProfileAndRole() error {
	err := utils.EnsureRole(a.iamService, a.deployer.RoleName)
	if err != nil {
		klog.Infof("error with ensure role: %v\n", err)
	}
	err = utils.EnsureInstanceProfile(a.iamService, a.deployer.InstanceProfile,
		a.deployer.RoleName)
	if err != nil {
		klog.Infof("error with ensure instance profile: %v\n", err)
	}
	return err
}

func (a *AWSRunner) prepareAWSImages() ([]utils.InternalAWSImage, error) {
	var ret []utils.InternalAWSImage

	var version string
	var err error
	if a.deployer.BuildOptions.CommonBuildOptions.StageVersion == "" {
		version, err = utils.SourceVersion(a.deployer.RepoRoot)
		if err != nil {
			return nil, fmt.Errorf("extracting version from repo %q, %w",
				a.deployer.BuildOptions.CommonBuildOptions.RepoRoot, err)
		}
	} else {
		version = a.deployer.BuildOptions.CommonBuildOptions.StageVersion
	}

	err = utils.ValidateS3Bucket(a.s3Service,
		a.deployer.BuildOptions.CommonBuildOptions.StageLocation,
		a.deployer.BuildOptions.CommonBuildOptions.StageVersion,
		version)
	if err != nil {
		return nil, fmt.Errorf("unable to validate s3 bucket : %w", err)
	}

	userControlPlane, err := a.getUserData(a.deployer.UserDataFile, version, true)
	if err != nil {
		return nil, fmt.Errorf("unable to load controlplane user data %s : %w", a.deployer.UserDataFile, err)
	}
	if len(userControlPlane) > 16384 { // 16KB
		return nil, fmt.Errorf("worker user data is too large, must be less than 16384 bytes, is %d\n\n%s", len(userControlPlane), userControlPlane)
	}

	userDataWorkerNode, err := a.getUserData(a.deployer.WorkerUserDataFile, version, false)
	if err != nil {
		return nil, fmt.Errorf("unable to load worker user data %s : %w", a.deployer.WorkerUserDataFile, err)
	}
	if len(userDataWorkerNode) > 16384 { // 16KB
		return nil, fmt.Errorf("worker user data is too large, must be less than 16384 bytes, is %d\n\n%s", len(userDataWorkerNode), userDataWorkerNode)
	}

	klog.Infof("using %s for control plane image", a.deployer.Image)
	klog.Infof("using %s for worker node image", a.deployer.WorkerImage)
	ret = append(ret, utils.InternalAWSImage{
		AmiID:           a.deployer.Image,
		UserData:        userControlPlane,
		InstanceType:    a.deployer.InstanceType,
		InstanceProfile: a.deployer.InstanceProfile,
	})
	for i := 0; i < a.deployer.NumNodes; i++ {
		ret = append(ret, utils.InternalAWSImage{
			AmiID:           a.deployer.WorkerImage,
			UserData:        userDataWorkerNode,
			InstanceType:    a.deployer.WorkerInstanceType,
			InstanceProfile: a.deployer.InstanceProfile,
		})
	}
	return ret, nil
}

func (a *AWSRunner) getUserData(dataFile string, version string, controlPlane bool) (string, error) {
	var userdata string
	if dataFile != "" {
		_, err := config.ConfigFS.Open(dataFile)
		if err == nil {
			userDataBytes, err := config.ConfigFS.ReadFile(dataFile)
			if err == nil {
				klog.Infof("loading user data from embedded file: %s", dataFile)
				userdata = string(userDataBytes)
			}
		}

		if userdata == "" {
			userDataBytes, err := os.ReadFile(dataFile)
			if err != nil {
				return "", fmt.Errorf("error reading userdata file %q, %w", dataFile, err)
			}
			klog.Infof("loading user data from file on disk: %s", dataFile)
			userdata = string(userDataBytes)
		}
	} else {
		userDataBytes, err := config.ConfigFS.ReadFile("ubuntu2404.yaml")
		if err != nil {
			return "", fmt.Errorf("error reading embedded ubuntu2404.yaml: %w", err)
		}
		klog.Infof("loading user data from embedded file: ubuntu2404.yaml")
		userdata = string(userDataBytes)
	}

	userdata = strings.ReplaceAll(userdata, "{{STAGING_BUCKET}}",
		a.deployer.BuildOptions.CommonBuildOptions.StageLocation)
	userdata = strings.ReplaceAll(userdata, "{{STAGING_VERSION}}", version)
	userdata = strings.ReplaceAll(userdata, "{{KUBEADM_TOKEN}}", a.token)
	userdata = strings.ReplaceAll(userdata, "{{KUBEADM_CERTIFICATE_KEY}}", a.certificateKey)
	userdata = strings.ReplaceAll(userdata, "{{KUBEADM_CLUSTER_ID}}", a.deployer.ClusterID)

	script, err := utils.FetchConfigureScript(dataFile, func(data string) string {
		data = strings.ReplaceAll(data, "{{CONTAINERD_PULL_REFS}}", os.Getenv("CONTAINERD_PULL_REFS"))
		return data
	})
	if err != nil {
		return "", fmt.Errorf("unable to fetch script : %w", err)
	}
	userdata = strings.ReplaceAll(userdata, "{{CONFIGURE_SH}}", script)

	provider := ""
	if a.deployer.ExternalCloudProvider {
		provider = "external"
	}

	loadBalancer := false
	if a.deployer.ExternalLoadBalancer {
		loadBalancer = true
	}

	yamlString, err := utils.FetchKubeadmInitYaml(a.deployer.KubeadmInitFile, func(data string) string {
		data = strings.ReplaceAll(data, "{{EXTERNAL_CLOUD_PROVIDER}}", provider)
		data = strings.ReplaceAll(data, "{{RUNTIME_CONFIG}}", a.deployer.RuntimeConfig)
		data = strings.ReplaceAll(data, "{{FEATURE_GATES}}", a.deployer.FeatureGates)
		return data
	})
	if err != nil {
		return "", fmt.Errorf("unable to fetch kubeadm-init.yaml : %w", err)
	}
	userdata = strings.ReplaceAll(userdata, "{{KUBEADM_INIT_YAML}}", yamlString)

	yamlString, err = utils.FetchKubeadmJoinYaml(a.deployer.KubeadmJoinFile, func(data string) string {
		data = strings.ReplaceAll(data, "{{EXTERNAL_CLOUD_PROVIDER}}", provider)
		data = strings.ReplaceAll(data, "{{FEATURE_GATES}}", a.deployer.FeatureGates)
		return data
	})
	if err != nil {
		return "", fmt.Errorf("unable to fetch kubeadm-join.yaml : %w", err)
	}
	userdata = strings.ReplaceAll(userdata, "{{KUBEADM_JOIN_YAML}}", yamlString)

	scriptString, err := utils.FetchRunKubeadmSH(func(data string) string {
		data = strings.ReplaceAll(data, "{{STAGING_BUCKET}}",
			a.deployer.BuildOptions.CommonBuildOptions.StageLocation)
		data = strings.ReplaceAll(data, "{{STAGING_VERSION}}", version)
		data = strings.ReplaceAll(data, "{{KUBEADM_TOKEN}}", a.token)
		data = strings.ReplaceAll(data, "{{KUBEADM_CERTIFICATE_KEY}}", a.certificateKey)
		return data
	})
	if err != nil {
		return "", fmt.Errorf("unable to fetch run-kubeadm.sh : %w", err)
	}
	userdata = strings.ReplaceAll(userdata, "{{RUN_KUBEADM_SH}}", scriptString)

	userdata = strings.ReplaceAll(userdata, "{{CONTAINERD_INSTALL_SERVICE}}", utils.FetchUbuntuFile("ubuntu/containerd-installation.service"))
	userdata = strings.ReplaceAll(userdata, "{{CONTAINERD_SERVICE}}", utils.FetchUbuntuFile("ubuntu/containerd.service"))
	userdata = strings.ReplaceAll(userdata, "{{CONTAINERD_TARGET}}", utils.FetchUbuntuFile("ubuntu/containerd.target"))
	userdata = strings.ReplaceAll(userdata, "{{KUBEADM_CONF}}", utils.FetchUbuntuFile("ubuntu/10-kubeadm.conf"))
	userdata = strings.ReplaceAll(userdata, "{{KUBELET_SERVICE}}", utils.FetchUbuntuFile("ubuntu/kubelet.service"))
	userdata = strings.ReplaceAll(userdata, "{{CREDENTIAL_PROVIDER_YAML}}", utils.FetchUbuntuFile("ubuntu/credential-provider.yaml"))

	userdata = strings.ReplaceAll(userdata, "{{FEATURE_GATES}}", a.deployer.FeatureGates)
	userdata = strings.ReplaceAll(userdata, "{{EXTERNAL_CLOUD_PROVIDER}}", provider)
	userdata = strings.ReplaceAll(userdata, "{{EXTERNAL_CLOUD_PROVIDER_IMAGE}}", a.deployer.ExternalCloudProviderImage)

	if loadBalancer {
		userdata = strings.ReplaceAll(userdata, "{{EXTERNAL_LOAD_BALANCER}}", "true")
	} else {
		userdata = strings.ReplaceAll(userdata, "{{EXTERNAL_LOAD_BALANCER}}", "false")
	}

	if a.deployer.DevicePluginNvidia {
		userdata = strings.ReplaceAll(userdata, "{{ENABLE_NVIDIA_DEVICE_PLUGIN}}", "true")
	} else {
		userdata = strings.ReplaceAll(userdata, "{{ENABLE_NVIDIA_DEVICE_PLUGIN}}", "false")
	}

	scriptString, err = utils.FetchRunPostInstallSH(func(data string) string {
		data = strings.ReplaceAll(data, "{{FEATURE_GATES}}", a.deployer.FeatureGates)
		data = strings.ReplaceAll(data, "{{EXTERNAL_CLOUD_PROVIDER}}", provider)
		data = strings.ReplaceAll(data, "{{EXTERNAL_CLOUD_PROVIDER_IMAGE}}", a.deployer.ExternalCloudProviderImage)

		if loadBalancer {
			data = strings.ReplaceAll(data, "{{EXTERNAL_LOAD_BALANCER}}", "true")
		} else {
			data = strings.ReplaceAll(data, "{{EXTERNAL_LOAD_BALANCER}}", "false")
		}

		if a.deployer.DevicePluginNvidia {
			data = strings.ReplaceAll(data, "{{ENABLE_NVIDIA_DEVICE_PLUGIN}}", "true")
		} else {
			data = strings.ReplaceAll(data, "{{ENABLE_NVIDIA_DEVICE_PLUGIN}}", "false")
		}

		return data
	})
	if err != nil {
		return "", fmt.Errorf("unable to fetch run-post-install.sh : %w", err)
	}
	userdata = strings.ReplaceAll(userdata, "{{RUN_POST_INSTALL_SH}}", scriptString)

	if controlPlane {
		userdata = strings.ReplaceAll(userdata, "{{KUBEADM_CONTROL_PLANE}}", "true")
	} else {
		userdata = strings.ReplaceAll(userdata, "{{KUBEADM_CONTROL_PLANE}}", "false")
	}
	return userdata, nil
}

func (a *AWSRunner) createAWSInstance(img utils.InternalAWSImage) (*awsInstance, error) {
	if a.deployer.SSHUser == "" {
		return nil, fmt.Errorf("please set '--ssh-user' parameter")
	} else {
		err := flag.Set("ssh-user", a.deployer.SSHUser)
		if err != nil {
			return nil, fmt.Errorf("unable to set flag ssh-user: %w", err)
		}
		err = flag.Set("ssh-env", "aws")
		if err != nil {
			return nil, fmt.Errorf("unable to set flag ssh-env: %w", err)
		}
	}

	if a.subnetID == "" {
		var err error
		var vpcID string
		a.subnetID, vpcID, err = utils.PickSubnetID(a.ec2Service)
		if err != nil {
			return nil, fmt.Errorf("picking subnet: %w in vpc (%s)", err, vpcID)
		}
	}

	var instance *ec2typesv2.Instance
	newInstance, err := utils.LaunchNewInstance(
		a.ec2Service,
		a.iamService,
		a.deployer.ClusterID,
		a.controlPlaneIP,
		img,
		a.subnetID)
	if err != nil {
		return nil, fmt.Errorf("unable to launch instance : %w", err)
	}
	instance = newInstance
	klog.Infof("launched new instance %s with ami-id: %s on instance type: %s",
		*instance.InstanceId, *instance.ImageId, instance.InstanceType)

	if instance.PublicIpAddress == nil {
		return nil, fmt.Errorf("missing public ip address for instance id : %s", *instance.InstanceId)
	}
	if instance.PrivateIpAddress == nil {
		return nil, fmt.Errorf("missing private ip address for instance id : %s", *instance.InstanceId)
	}
	return &awsInstance{
		instanceID: *instance.InstanceId,
		instance:   instance,
		publicIP:   *instance.PublicIpAddress,
		privateIP:  *instance.PrivateIpAddress,
	}, nil
}

// assignNewSSHKey generates a new SSH key-pair and assigns it to the EC2 instance using EC2-instance connect. It then
// connects via SSH and makes the key permanent by writing it to ~/.ssh/authorized_keys
func (a *AWSRunner) assignNewSSHKey(testInstance *awsInstance) error {
	var key *utils.TemporarySSHKey
	var err error

	if utils.LocalSSHKeyExists("id_rsa") {
		klog.Info("loading existing id_rsa key")
		key, err = utils.LoadExistingSSHKey("id_rsa")
		if err != nil {
			return fmt.Errorf("error loading existing id_rsa SSH key, %w", err)
		}
	}
	if key == nil && utils.LocalSSHKeyExists("id_ed25519") {
		klog.Info("loading existing id_ed25519 key")
		key, err = utils.LoadExistingSSHKey("id_ed25519")
		if err != nil {
			return fmt.Errorf("error loading existing id_ed25519 SSH key, %w", err)
		}
	}
	if key == nil {
		// create our new key
		klog.Infof("assigning new SSH key-pair for %s@%s", a.deployer.SSHUser, testInstance.publicIP)
		key, err = utils.GenerateSSHKeypair()
		if err != nil {
			return fmt.Errorf("creating SSH key, %w", err)
		}
	}
	testInstance.sshKey = key
	_, err = a.ec2icService.SendSSHPublicKey(context.TODO(), &ec2instanceconnectv2.SendSSHPublicKeyInput{
		InstanceId:       awsv2.String(testInstance.instanceID),
		InstanceOSUser:   awsv2.String(a.deployer.SSHUser),
		SSHPublicKey:     awsv2.String(string(key.Public)),
		AvailabilityZone: testInstance.instance.Placement.AvailabilityZone,
	})
	if err != nil {
		return fmt.Errorf("sending SSH Public key for serial console access for %s, %w", a.deployer.SSHUser, err)
	}
	klog.Infof("dialing ssh %s@%s", a.deployer.SSHUser, testInstance.publicIP)
	client, err := ssh.Dial("tcp", fmt.Sprintf("%s:22", testInstance.publicIP), &ssh.ClientConfig{
		User:            a.deployer.SSHUser,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(key.Signer),
		},
	})
	if err != nil {
		return fmt.Errorf("dialing SSH %s@%s %w", a.deployer.SSHUser, testInstance.publicIP, err)
	}

	// add our ssh key to authorized keys so it will last longer than 60 seconds
	sess, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("creating SSH sess, %w", err)
	}

	_, err = sess.CombinedOutput(fmt.Sprintf("echo '%s' >> ~/.ssh/authorized_keys", string(testInstance.sshKey.Public)))
	if err != nil {
		return fmt.Errorf("registering SSH key, %w", err)
	}

	if testInstance.sshKey.PrivateKeyPath == "" {
		// write our Private SSH key to disk and register it
		f, err := os.CreateTemp("", ".ssh-key-*")
		if err != nil {
			return fmt.Errorf("creating SSH key, %w", err)
		}
		sshKeyFile := f.Name()
		if err = os.Chmod(sshKeyFile, 0400); err != nil {
			return fmt.Errorf("chmod'ing SSH key, %w", err)
		}

		if _, err = f.Write(testInstance.sshKey.Private); err != nil {
			return fmt.Errorf("writing SSH key, %w", err)
		}
		testInstance.sshKey.PrivateKeyPath = sshKeyFile
	}
	remote.AddSSHKey(testInstance.instanceID, testInstance.sshKey.PrivateKeyPath)
	testInstance.sshPublicKeyFile = testInstance.sshKey.PrivateKeyPath
	return nil
}
