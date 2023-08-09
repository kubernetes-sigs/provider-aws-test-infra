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
	crand "crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"flag"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ec2instanceconnect"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/aws/aws-sdk-go/service/ssm"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"

	"k8s.io/klog/v2"
	"sigs.k8s.io/kubetest2/pkg/exec"

	"sigs.k8s.io/provider-aws-test-infra/kubetest2-ec2/pkg/deployer/remote"
	"sigs.k8s.io/provider-aws-test-infra/kubetest2-ec2/pkg/deployer/utils"
)

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

	for _, image := range runner.internalAWSImages {
		instance, err := runner.getAWSInstance(image)
		if err != nil {
			return err
		}
		klog.Infof("starting instance id: %s", instance.instanceID)
		runner.instances = append(runner.instances, instance)
	}
	return nil
}

const amiIDTag = "Node-E2E-Test"

type AWSRunner struct {
	deployer           *deployer
	ec2Service         *ec2.EC2
	ec2icService       *ec2instanceconnect.EC2InstanceConnect
	ssmService         *ssm.SSM
	instanceNamePrefix string
	internalAWSImages  []internalAWSImage
	instances          []*awsInstance
	token              string
	controlPlaneIP     string
}

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
	sshKey           *temporarySSHKey
	publicIP         string
	sshPublicKeyFile string
}

type temporarySSHKey struct {
	public  []byte
	private []byte
	signer  ssh.Signer
}

func (d *deployer) NewAWSRunner() *AWSRunner {
	d.runner = &AWSRunner{
		deployer:           d,
		instanceNamePrefix: "tmp-e2e-" + uuid.New().String()[:8],
		token:              utils.RandomFixedLengthString(6) + "." + utils.RandomFixedLengthString(16),
	}
	return d.runner
}

func (a *AWSRunner) Validate() error {
	if len(a.deployer.Images) == 0 {
		klog.Fatalf("Must specify --images.")
	}
	for _, img := range a.deployer.Images {
		if !strings.HasPrefix(img, "ami-") {
			return fmt.Errorf("invalid AMI id format for %q", img)
		}
	}
	sess, err := session.NewSession(&aws.Config{Region: &a.deployer.Region})
	if err != nil {
		klog.Fatalf("Unable to create AWS session, %s", err)
	}
	a.ec2Service = ec2.New(sess)
	a.ec2icService = ec2instanceconnect.New(sess)
	a.ssmService = ssm.New(sess)
	s3Uploader := s3manager.NewUploaderWithClient(s3.New(sess), func(u *s3manager.Uploader) {
		u.PartSize = 10 * 1024 * 1024 // 50 mb
		u.Concurrency = 10
	})
	a.deployer.BuildOptions.CommonBuildOptions.S3Uploader = s3Uploader
	if a.internalAWSImages, err = a.prepareAWSImages(); err != nil {
		klog.Fatalf("While preparing AWS images: %v", err)
	}
	return nil
}

func (a *AWSRunner) isAWSInstanceRunning(testInstance *awsInstance) (*awsInstance, error) {
	instanceRunning := false
	createdSSHKey := false
	for i := 0; i < 30 && !instanceRunning; i++ {
		if i > 0 {
			time.Sleep(time.Second * 10)
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
		return nil, fmt.Errorf("instance %s is not running", testInstance.instanceID)
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

func (a *AWSRunner) prepareAWSImages() ([]internalAWSImage, error) {
	var ret []internalAWSImage

	var userControlPlane string
	var userDataWorkerNode string
	if a.deployer.UserDataFile != "" {
		userDataBytes, err := os.ReadFile(a.deployer.UserDataFile)
		if err != nil {
			return nil, fmt.Errorf("reading userdata file %q, %w", a.deployer.UserDataFile, err)
		}
		var userdata = string(userDataBytes)

		if a.deployer.BuildOptions.CommonBuildOptions.StageLocation == "" {
			return nil, fmt.Errorf("please specify --stage with the s3 bucket")
		}

		userdata = strings.ReplaceAll(userdata, "{{STAGING_BUCKET}}",
			a.deployer.BuildOptions.CommonBuildOptions.StageLocation)
		version, err := utils.SourceVersion(a.deployer.BuildOptions.CommonBuildOptions.RepoRoot)
		if err != nil {
			return nil, fmt.Errorf("extracting version from repo %q, %w",
				a.deployer.BuildOptions.CommonBuildOptions.RepoRoot, err)
		}
		userdata = strings.ReplaceAll(userdata, "{{STAGING_VERSION}}", version)

		userdata = strings.ReplaceAll(userdata, "{{KUBEADM_TOKEN}}", a.token)
		userControlPlane = strings.ReplaceAll(userdata, "{{KUBEADM_CONTROL_PLANE}}", "true")
		userDataWorkerNode = strings.ReplaceAll(userdata, "{{KUBEADM_CONTROL_PLANE}}", "false")
	}

	if len(a.deployer.Images) > 0 {
		for _, imageID := range a.deployer.Images {
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
		}
	}
	return ret, nil
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
	klog.Infof("launched new instance %s with ami-id: %s", *instance.InstanceId, *instance.ImageId)

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
	key, err := generateSSHKeypair()
	if err != nil {
		return fmt.Errorf("creating SSH key, %w", err)
	}
	testInstance.sshKey = key
	_, err = a.ec2icService.SendSSHPublicKey(&ec2instanceconnect.SendSSHPublicKeyInput{
		InstanceId:       aws.String(testInstance.instanceID),
		InstanceOSUser:   aws.String(a.deployer.SSHUser),
		SSHPublicKey:     aws.String(string(key.public)),
		AvailabilityZone: testInstance.instance.Placement.AvailabilityZone,
	})
	if err != nil {
		return fmt.Errorf("sending SSH public key for serial console access for %s, %w", a.deployer.SSHUser, err)
	}
	client, err := ssh.Dial("tcp", fmt.Sprintf("%s:22", testInstance.publicIP), &ssh.ClientConfig{
		User:            a.deployer.SSHUser,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(key.signer),
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

	_, err = sess.CombinedOutput(fmt.Sprintf("echo '%s' >> ~/.ssh/authorized_keys", string(testInstance.sshKey.public)))
	if err != nil {
		return fmt.Errorf("registering SSH key, %w", err)
	}

	// write our private SSH key to disk and register it
	f, err := os.CreateTemp("", ".ssh-key-*")
	if err != nil {
		return fmt.Errorf("creating SSH key, %w", err)
	}
	sshKeyFile := f.Name()
	if err = os.Chmod(sshKeyFile, 0400); err != nil {
		return fmt.Errorf("chmod'ing SSH key, %w", err)
	}

	if _, err = f.Write(testInstance.sshKey.private); err != nil {
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
		input.IamInstanceProfile = &ec2.IamInstanceProfileSpecification{
			Name: &img.instanceProfile,
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

func generateSSHKeypair() (*temporarySSHKey, error) {
	privateKey, err := rsa.GenerateKey(crand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generating private key, %w", err)
	}
	if err := privateKey.Validate(); err != nil {
		return nil, fmt.Errorf("validating private key, %w", err)
	}

	pubSSH, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("creating SSH key, %w", err)
	}
	pubKey := ssh.MarshalAuthorizedKey(pubSSH)

	privDER := x509.MarshalPKCS1PrivateKey(privateKey)
	privBlock := pem.Block{
		Type:    "RSA PRIVATE KEY",
		Headers: nil,
		Bytes:   privDER,
	}
	privatePEM := pem.EncodeToMemory(&privBlock)

	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("creating signer, %w", err)
	}
	return &temporarySSHKey{
		public:  pubKey,
		private: privatePEM,
		signer:  signer,
	}, nil
}
