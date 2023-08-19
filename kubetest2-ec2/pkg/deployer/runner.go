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
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ec2instanceconnect"
	"github.com/aws/aws-sdk-go/service/iam"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/aws/aws-sdk-go/service/ssm"

	"golang.org/x/crypto/ssh"
	"golang.org/x/exp/maps"

	"k8s.io/klog/v2"

	"sigs.k8s.io/provider-aws-test-infra/kubetest2-ec2/config"
	"sigs.k8s.io/provider-aws-test-infra/kubetest2-ec2/pkg/deployer/remote"
	"sigs.k8s.io/provider-aws-test-infra/kubetest2-ec2/pkg/deployer/utils"
)

type AWSRunner struct {
	deployer           *deployer
	ec2Service         *ec2.EC2
	ec2icService       *ec2instanceconnect.EC2InstanceConnect
	ssmService         *ssm.SSM
	iamService         *iam.IAM
	s3Service          *s3.S3
	instanceNamePrefix string
	internalAWSImages  []internalAWSImage
	instances          []*awsInstance
	token              string
	controlPlaneIP     string
}

type internalAWSImage struct {
	amiID string
	// The instance type (e.g. t3a.medium)
	instanceType string
	userData     string
	imageDesc    string
	// name of the instance profile
	instanceProfile string
}

type awsInstance struct {
	instance         *ec2.Instance
	instanceID       string
	sshKey           *utils.TemporarySSHKey
	publicIP         string
	sshPublicKeyFile string
}

func (a *AWSRunner) Validate() error {
	sess, err := a.InitializeServices()
	if err != nil {
		return fmt.Errorf("unable to initialize AWS services : %w", err)
	}

	bucket := a.deployer.BuildOptions.CommonBuildOptions.StageLocation
	if bucket == "" {
		return fmt.Errorf("please specify --stage with the s3 bucket")
	}
	if !strings.Contains(bucket, "://") {
		_, err = a.s3Service.HeadBucket(&s3.HeadBucketInput{Bucket: aws.String(bucket)})
		if err != nil {
			return fmt.Errorf("unable to find bucket %q, %v", bucket, err)
		}
	}

	if a.deployer.Image == "" {
		arch := strings.Split(a.deployer.BuildOptions.CommonBuildOptions.TargetBuildArch, "/")[1]
		path := "/aws/service/canonical/ubuntu/server/jammy/stable/current/" + arch + "/hvm/ebs-gp2/ami-id"
		klog.Infof("image was not specified, looking up latest image in SSM:")
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
	}

	if len(a.deployer.Image) == 0 {
		return fmt.Errorf("must specify an Ubuntu AMI using --image")
	}

	if !strings.HasPrefix(a.deployer.Image, "ami-") {
		return fmt.Errorf("invalid AMI id format for %q", a.deployer.Image)
	}

	if err = a.ensureInstanceProfileAndRole(sess); err != nil {
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
	for i := 0; i < 30 && !instanceRunning; i++ {
		if i > 0 {
			time.Sleep(time.Second * 15)
		}

		var op *ec2.DescribeInstancesOutput
		op, err := a.ec2Service.DescribeInstances(&ec2.DescribeInstancesInput{
			InstanceIds: []*string{&testInstance.instanceID},
		})
		if err != nil {
			continue
		}
		instance := op.Reservations[0].Instances[0]
		if *instance.State.Name != ec2.InstanceStateNameRunning {
			continue
		}
		testInstance.publicIP = *instance.PublicIpAddress
		if a.controlPlaneIP == "" {
			a.controlPlaneIP = *testInstance.instance.PrivateIpAddress
		}

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
			output, err := remote.SSH(testInstance.instanceID, "kubectl --kubeconfig /etc/kubernetes/admin.conf wait --for=condition=ready nodes --timeout=5m --all")
			if err != nil {
				return nil, fmt.Errorf("checking instance %s is not ready - Command failed: %s", testInstance.instanceID, output)
			}
			output, err = remote.SSH(testInstance.instanceID, "kubectl --kubeconfig /etc/kubernetes/admin.conf taint nodes --all node-role.kubernetes.io/control-plane:NoSchedule-")
			if err != nil {
				return nil, fmt.Errorf("unable to remove taints for nodes in %s - Command failed: %s", testInstance.instanceID, output)
			}
			if a.deployer.KubeconfigPath == "" {
				a.deployer.KubeconfigPath = downloadKubeConfig(testInstance.instanceID, testInstance.publicIP)
			}
		}
	}
	klog.Infof("instance %s is running", testInstance.instanceID)
	return testInstance, nil
}

func (a *AWSRunner) InitializeServices() (*session.Session, error) {
	sess, err := session.NewSession(&aws.Config{Region: &a.deployer.Region})
	if err != nil {
		return nil, fmt.Errorf("unable to create AWS session, %w", err)
	}
	a.ec2Service = ec2.New(sess)
	a.ec2icService = ec2instanceconnect.New(sess)
	a.ssmService = ssm.New(sess)
	a.iamService = iam.New(sess, &aws.Config{Region: &a.deployer.Region})
	a.s3Service = s3.New(sess)
	a.deployer.BuildOptions.CommonBuildOptions.S3Uploader = s3manager.NewUploaderWithClient(a.s3Service, func(u *s3manager.Uploader) {
		u.PartSize = 10 * 1024 * 1024 // 50 mb
		u.Concurrency = 10
	})
	return sess, nil
}

func (a *AWSRunner) ensureInstanceProfileAndRole(sess *session.Session) error {
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

func (a *AWSRunner) prepareAWSImages() ([]internalAWSImage, error) {
	var ret []internalAWSImage

	var userControlPlane string
	var userDataWorkerNode string
	var userdata string
	if a.deployer.UserDataFile != "" {
		userDataBytes, err := os.ReadFile(a.deployer.UserDataFile)
		if err != nil {
			return nil, fmt.Errorf("error reading userdata file %q, %w", a.deployer.UserDataFile, err)
		}
		userdata = string(userDataBytes)
	} else {
		userDataBytes, err := config.ConfigFS.ReadFile("ubuntu2204.yaml")
		if err != nil {
			return nil, fmt.Errorf("error reading embedded ubuntu2204.yaml: %w", err)
		}
		userdata = string(userDataBytes)
	}

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

	err = a.validateS3Bucket(version)
	if err != nil {
		return nil, fmt.Errorf("unable to validate s3 bucket : %w", err)
	}

	userdata = strings.ReplaceAll(userdata, "{{STAGING_BUCKET}}",
		a.deployer.BuildOptions.CommonBuildOptions.StageLocation)
	userdata = strings.ReplaceAll(userdata, "{{STAGING_VERSION}}", version)
	userdata = strings.ReplaceAll(userdata, "{{KUBEADM_TOKEN}}", a.token)

	script, err := a.fetchConfigureScript()
	if err != nil {
		return nil, fmt.Errorf("unable to fetch script : %w", err)
	}
	userdata = strings.ReplaceAll(userdata, "{{CONFIGURE_SH}}", script)

	yamlBytes, err := config.ConfigFS.ReadFile("kubeadm-init.yaml")
	if err != nil {
		return nil, fmt.Errorf("error reading kubeadm-init.yaml: %w", err)
	}
	yamlString, err := gzipAndBase64Encode(yamlBytes)
	if err != nil {
		return nil, fmt.Errorf("error reading kubeadm-init.yaml: %w", err)
	}
	userdata = strings.ReplaceAll(userdata, "{{KUBEADM_INIT_YAML}}", yamlString)

	yamlBytes, err = config.ConfigFS.ReadFile("kubeadm-join.yaml")
	if err != nil {
		return nil, fmt.Errorf("error reading kubeadm-join.yaml: %w", err)
	}
	yamlString, err = gzipAndBase64Encode(yamlBytes)
	if err != nil {
		return nil, fmt.Errorf("error reading kubeadm-join.yaml: %w", err)
	}
	userdata = strings.ReplaceAll(userdata, "{{KUBEADM_JOIN_YAML}}", yamlString)

	userControlPlane = strings.ReplaceAll(userdata, "{{KUBEADM_CONTROL_PLANE}}", "true")
	userDataWorkerNode = strings.ReplaceAll(userdata, "{{KUBEADM_CONTROL_PLANE}}", "false")

	imageID := a.deployer.Image
	ret = append(ret, internalAWSImage{
		amiID:           imageID,
		userData:        userControlPlane,
		instanceType:    a.deployer.InstanceType,
		instanceProfile: a.deployer.InstanceProfile,
	})
	for i := 0; i < a.deployer.NumNodes; i++ {
		ret = append(ret, internalAWSImage{
			amiID:           imageID,
			userData:        userDataWorkerNode,
			instanceType:    a.deployer.InstanceType,
			instanceProfile: a.deployer.InstanceProfile,
		})
	}
	return ret, nil
}

func (a *AWSRunner) validateS3Bucket(version string) error {
	if strings.Contains(a.deployer.BuildOptions.CommonBuildOptions.StageLocation, "://") {
		return nil
	}

	results, err := a.s3Service.ListObjectsV2(&s3.ListObjectsV2Input{
		Bucket: aws.String(a.deployer.BuildOptions.CommonBuildOptions.StageLocation),
		Prefix: aws.String(version),
	})
	if err != nil {
		return fmt.Errorf("version %s is missing from bucket %s: %w",
			a.deployer.BuildOptions.CommonBuildOptions.StageVersion,
			a.deployer.BuildOptions.CommonBuildOptions.StageLocation,
			err)
	} else if results.KeyCount == nil || *results.KeyCount == 0 {
		results, _ = a.s3Service.ListObjectsV2(&s3.ListObjectsV2Input{
			Bucket: aws.String(a.deployer.BuildOptions.CommonBuildOptions.StageLocation),
			Prefix: aws.String("v"),
		})

		availableVersions := map[string]string{}
		if results != nil && results.KeyCount != nil && *results.KeyCount > 0 {
			for _, item := range results.Contents {
				dir := strings.Split(*item.Key, "/")[0]
				if _, ok := availableVersions[dir]; !ok {
					availableVersions[dir] = *item.Key
				}
			}
		}

		return fmt.Errorf("version %s is missing from bucket %s, choose one of %s",
			a.deployer.BuildOptions.CommonBuildOptions.StageVersion,
			a.deployer.BuildOptions.CommonBuildOptions.StageLocation,
			maps.Keys(availableVersions))
	}
	return nil
}

func (a *AWSRunner) deleteAWSInstance(instanceID string) {
	klog.Infof("Terminating instance %q", instanceID)
	_, err := a.ec2Service.TerminateInstances(&ec2.TerminateInstancesInput{
		InstanceIds: []*string{&instanceID},
	})
	if err != nil {
		klog.Errorf("Error terminating instance %q: %v", instanceID, err)
	}
}

func (a *AWSRunner) getAWSInstance(img internalAWSImage) (*awsInstance, error) {
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
	// TODO: Throw an error or log a warning
	// first see if we have an instance already running the desired image
	_, err := a.ec2Service.DescribeInstances(&ec2.DescribeInstancesInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("instance-state-name"),
				Values: []*string{aws.String(ec2.InstanceStateNameRunning)},
			},
			{
				Name:   aws.String(fmt.Sprintf("tag:%s", amiIDTag)),
				Values: []*string{aws.String(img.amiID)},
			},
		},
	})
	if err != nil {
		return nil, err
	}

	var instance *ec2.Instance
	newInstance, err := a.launchNewInstance(img)
	if err != nil {
		return nil, err
	}
	instance = newInstance
	klog.Infof("launched new instance %s with ami-id: %s on instance type: %s",
		*instance.InstanceId, *instance.ImageId, *instance.InstanceType)

	testInstance := &awsInstance{
		instanceID: *instance.InstanceId,
		instance:   instance,
	}

	return a.isAWSInstanceRunning(testInstance)
}

// assignNewSSHKey generates a new SSH key-pair and assigns it to the EC2 instance using EC2-instance connect. It then
// connects via SSH and makes the key permanent by writing it to ~/.ssh/authorized_keys
func (a *AWSRunner) assignNewSSHKey(testInstance *awsInstance) error {
	// create our new key
	key, err := utils.GenerateSSHKeypair()
	if err != nil {
		return fmt.Errorf("creating SSH key, %w", err)
	}
	testInstance.sshKey = key
	_, err = a.ec2icService.SendSSHPublicKey(&ec2instanceconnect.SendSSHPublicKeyInput{
		InstanceId:       aws.String(testInstance.instanceID),
		InstanceOSUser:   aws.String(a.deployer.SSHUser),
		SSHPublicKey:     aws.String(string(key.Public)),
		AvailabilityZone: testInstance.instance.Placement.AvailabilityZone,
	})
	if err != nil {
		return fmt.Errorf("sending SSH Public key for serial console access for %s, %w", a.deployer.SSHUser, err)
	}
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
	remote.AddSSHKey(testInstance.instanceID, sshKeyFile)
	testInstance.sshPublicKeyFile = sshKeyFile
	return nil
}

func (a *AWSRunner) launchNewInstance(img internalAWSImage) (*ec2.Instance, error) {
	images, err := a.ec2Service.DescribeImages(&ec2.DescribeImagesInput{ImageIds: []*string{&img.amiID}})
	if err != nil {
		return nil, fmt.Errorf("describing images: %w in region (%s)", err, *a.ec2Service.Config.Region)
	}

	input := &ec2.RunInstancesInput{
		InstanceType: &img.instanceType,
		ImageId:      &img.amiID,
		MinCount:     aws.Int64(1),
		MaxCount:     aws.Int64(1),
		NetworkInterfaces: []*ec2.InstanceNetworkInterfaceSpecification{
			{
				AssociatePublicIpAddress: aws.Bool(true),
				DeviceIndex:              aws.Int64(0),
			},
		},
		TagSpecifications: []*ec2.TagSpecification{
			{
				ResourceType: aws.String(ec2.ResourceTypeInstance),
				Tags: []*ec2.Tag{
					{
						Key:   aws.String("Name"),
						Value: aws.String(a.instanceNamePrefix + img.imageDesc),
					},
					// tagged so we can find it easily
					{
						Key:   aws.String(amiIDTag),
						Value: aws.String(img.amiID),
					},
				},
			},
			{
				ResourceType: aws.String(ec2.ResourceTypeVolume),
				Tags: []*ec2.Tag{
					{
						Key:   aws.String("Name"),
						Value: aws.String(a.instanceNamePrefix + img.imageDesc),
					},
				},
			},
		},
		BlockDeviceMappings: []*ec2.BlockDeviceMapping{
			{
				DeviceName: aws.String(*images.Images[0].RootDeviceName),
				Ebs: &ec2.EbsBlockDevice{
					VolumeSize: aws.Int64(50),
					VolumeType: aws.String("gp3"),
				},
			},
		},
	}
	if len(img.userData) > 0 {
		data := img.userData
		if a.controlPlaneIP != "" {
			data = strings.ReplaceAll(data, "{{KUBEADM_CONTROL_PLANE_IP}}", a.controlPlaneIP)
		}
		input.UserData = aws.String(base64.StdEncoding.EncodeToString([]byte(data)))
	}
	if img.instanceProfile != "" {
		arn, err := utils.GetInstanceProfileArn(a.iamService, img.instanceProfile)
		if err != nil {
			return nil, fmt.Errorf("getting instance profile arn, %w", err)
		}
		input.IamInstanceProfile = &ec2.IamInstanceProfileSpecification{
			Arn: aws.String(arn),
		}
	}

	rsv, err := a.ec2Service.RunInstances(input)
	if err != nil {
		return nil, fmt.Errorf("creating instance, %w", err)
	}

	return rsv.Instances[0], nil
}

func (a *AWSRunner) getSSMImage(path string) (string, error) {
	rsp, err := a.ssmService.GetParameter(&ssm.GetParameterInput{
		Name: &path,
	})
	if err != nil {
		return "", fmt.Errorf("getting AMI ID from SSM path %q, %w", path, err)
	}
	return *rsp.Parameter.Value, nil
}

func (a *AWSRunner) fetchConfigureScript() (string, error) {
	var scriptBytes []byte
	var err error
	if a.deployer.UserDataFile != "" {
		scriptFile := filepath.Dir(a.deployer.UserDataFile) + "/" + "configure.sh"
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