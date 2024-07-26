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

package aws

import (
	"context"
	crand "crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2instanceconnect"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/test/e2e_node/remote"
	"sigs.k8s.io/yaml"
)

var _ remote.Runner = (*AWSRunner)(nil)

func init() {
	remote.RegisterRunner("aws", NewAWSRunner)
}

var region = flag.String("region", "", "AWS region that the hosts live in (aws)")
var userDataFile = flag.String("user-data-file", "", "Path to user data to pass to created instances (aws)")
var instanceProfile = flag.String("instance-profile", "", "The name of the instance profile to assign to the node (aws)")
var instanceConnect = flag.Bool("ec2-instance-connect", true, "Use EC2 instance connect to generate a one time use key (aws)")
var instanceType = flag.String("instance-type", "t3a.medium", "EC2 Instance type to use for test")
var reuseInstances = flag.Bool("reuse-instances", false, "Reuse already running instance")

const amiIDTag = "Node-E2E-Test"

type AWSRunner struct {
	cfg               remote.Config
	ec2Service        *ec2.Client
	ec2icService      *ec2instanceconnect.Client
	ssmService        *ssm.Client
	internalAWSImages []internalAWSImage
}

func NewAWSRunner(cfg remote.Config) remote.Runner {
	if cfg.InstanceNamePrefix == "" {
		cfg.InstanceNamePrefix = "tmp-node-e2e-" + uuid.New().String()[:8]
	}

	return &AWSRunner{cfg: cfg}
}

func (a *AWSRunner) Validate() error {
	if len(a.cfg.Hosts) == 0 && a.cfg.ImageConfigFile == "" && len(a.cfg.Images) == 0 {
		klog.Fatalf("Must specify one of --image-config-file, --hosts, --images.")
	}
	for _, img := range a.cfg.Images {
		if !strings.HasPrefix(img, "ami-") {
			return fmt.Errorf("invalid AMI id format for %q", img)
		}
	}

	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion(*region),
	)
	if err != nil {
		klog.Fatalf("Unable to load AWS default config, %s", err)
	}
	a.ec2Service = ec2.NewFromConfig(cfg)
	a.ec2icService = ec2instanceconnect.NewFromConfig(cfg)
	a.ssmService = ssm.NewFromConfig(cfg)
	if a.internalAWSImages, err = a.prepareAWSImages(); err != nil {
		klog.Fatalf("While preparing AWS images: %v", err)
	}
	return nil
}

func (a *AWSRunner) StartTests(suite remote.TestSuite, archivePath string, results chan *remote.TestResult) (numTests int) {
	for i := range a.internalAWSImages {
		img := a.internalAWSImages[i]
		fmt.Printf("Initializing e2e tests using image %s / %s.\n", img.imageDesc, img.amiID)
		numTests++
		go func() {
			results <- a.testAWSImage(suite, archivePath, img)
		}()
	}
	return
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
	userData     []byte
	imageDesc    string
	// name of the instance profile
	instanceProfile string
}

func (a *AWSRunner) prepareAWSImages() ([]internalAWSImage, error) {
	var ret []internalAWSImage
	var userData []byte
	var err error

	// Parse images from given config file and convert them to internalGCEImage.
	if a.cfg.ImageConfigFile != "" {
		configPath := a.cfg.ImageConfigFile
		if a.cfg.ImageConfigDir != "" {
			configPath = filepath.Join(a.cfg.ImageConfigDir, a.cfg.ImageConfigFile)
		}

		imageConfigData, err := os.ReadFile(configPath)
		if err != nil {
			return nil, fmt.Errorf("could not read image config file provided: %w", err)
		}
		externalImageConfig := AWSImageConfig{Images: make(map[string]AWSImage)}
		err = yaml.Unmarshal(imageConfigData, &externalImageConfig)
		if err != nil {
			return nil, fmt.Errorf("could not parse image config file: %w", err)
		}

		for shortName, imageConfig := range externalImageConfig.Images {
			var amiID string
			if imageConfig.SSMPath != "" && imageConfig.AmiID == "" {
				amiID, err = a.getSSMImage(imageConfig.SSMPath)
				if err != nil {
					return nil, fmt.Errorf("could not retrieve a image based on SSM path %s, %w", imageConfig.SSMPath, err)
				}
			} else {
				amiID = imageConfig.AmiID
			}

			// user data can only be from an image config or the command line
			if *userDataFile != "" && imageConfig.UserData != "" {
				return nil, fmt.Errorf("can't specify userdata on both the command line and in an image config")
			}

			imageUserDataFile := *userDataFile
			if imageUserDataFile == "" && imageConfig.UserData != "" {
				imageUserDataFile = filepath.Join(a.cfg.ImageConfigDir, imageConfig.UserData)
			}
			if imageUserDataFile != "" {
				userData, err = readUserdata(imageUserDataFile)
				if err != nil {
					return nil, err
				}
			}

			// the instance profile can from image config or the command line
			if *instanceProfile != "" && imageConfig.InstanceProfile != "" {
				return nil, fmt.Errorf("can't specify instance profile on both the command line and in an image config")
			}
			instanceProfile := *instanceProfile
			if instanceProfile == "" {
				instanceProfile = imageConfig.InstanceProfile
			}

			awsImage := internalAWSImage{
				amiID:           amiID,
				userData:        userData,
				instanceType:    imageConfig.InstanceType,
				instanceProfile: instanceProfile,
				imageDesc:       shortName,
			}
			if awsImage.instanceType == "" {
				awsImage.instanceType = *instanceType
			}
			ret = append(ret, awsImage)
		}
	}

	if len(a.cfg.Images) > 0 {
		if *userDataFile != "" {
			userData, err = readUserdata(*userDataFile)
			if err != nil {
				return nil, err
			}
		}
		for _, img := range a.cfg.Images {
			ret = append(ret, internalAWSImage{
				amiID:           img,
				instanceType:    *instanceType,
				instanceProfile: *instanceProfile,
				userData:        userData,
			})
		}
	}
	return ret, nil
}

func (a *AWSRunner) testAWSImage(suite remote.TestSuite, archivePath string, imageConfig internalAWSImage) *remote.TestResult {
	instance, err := a.getAWSInstance(imageConfig)
	if err != nil {
		return &remote.TestResult{
			Err: fmt.Errorf("unable to create EC2 instance for image %s, %w", imageConfig.amiID, err),
		}
	}
	if a.cfg.DeleteInstances {
		defer a.deleteAWSInstance(instance.instanceID)
	}
	if instance.sshPublicKeyFile != "" && *instanceConnect {
		defer os.Remove(instance.sshPublicKeyFile)
	}
	deleteFiles := !a.cfg.DeleteInstances && a.cfg.Cleanup
	ginkgoFlagsStr := a.cfg.GinkgoFlags

	output, exitOk, err := remote.RunRemote(remote.RunRemoteConfig{
		Suite:          suite,
		Archive:        archivePath,
		Host:           instance.instanceID,
		Cleanup:        deleteFiles,
		ImageDesc:      imageConfig.amiID,
		JunitFileName:  instance.instanceID,
		TestArgs:       a.cfg.TestArgs,
		GinkgoArgs:     ginkgoFlagsStr,
		SystemSpecName: a.cfg.SystemSpecName,
		ExtraEnvs:      a.cfg.ExtraEnvs,
		RuntimeConfig:  a.cfg.RuntimeConfig,
	})
	return &remote.TestResult{
		Output: output,
		Err:    err,
		Host:   instance.instanceID,
		ExitOK: exitOk,
	}
}

func (a *AWSRunner) deleteAWSInstance(instanceID string) {
	klog.Infof("Terminating instance %q", instanceID)
	_, err := a.ec2Service.TerminateInstances(context.TODO(), &ec2.TerminateInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		klog.Errorf("Error terminating instance %q: %v", instanceID, err)
	}
}

func (a *AWSRunner) getAWSInstance(img internalAWSImage) (*awsInstance, error) {
	// first see if we have an instance already running the desired image
	existing, err := a.ec2Service.DescribeInstances(context.TODO(), &ec2.DescribeInstancesInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("instance-state-name"),
				Values: []string{string(types.InstanceStateNameRunning)},
			},
			{
				Name:   aws.String(fmt.Sprintf("tag:%s", amiIDTag)),
				Values: []string{img.amiID},
			},
		},
	})
	if err != nil {
		return nil, err
	}

	var instance *types.Instance
	if *reuseInstances && len(existing.Reservations) > 0 && len(existing.Reservations[0].Instances) > 0 {
		instance = &existing.Reservations[0].Instances[0]
		klog.Infof("reusing existing instance %s", *instance.InstanceId)
	} else {
		// no existing instance running that image, so we need to launch a new instance
		newInstance, err := a.launchNewInstance(img)
		if err != nil {
			return nil, err
		}
		instance = newInstance
		klog.Infof("launched new instance %s with ami-id: %s", *instance.InstanceId, *instance.ImageId)
	}

	testInstance := &awsInstance{
		instanceID: *instance.InstanceId,
		instance:   instance,
	}

	klog.Infof("waiting for %s to start (5 mins)", testInstance.instanceID)
	err = ec2.NewInstanceRunningWaiter(a.ec2Service).Wait(context.TODO(),
		&ec2.DescribeInstancesInput{
			InstanceIds: []string{testInstance.instanceID},
		}, 5*time.Minute)

	if err != nil {
		return testInstance, fmt.Errorf("instance %s did not start running", testInstance.instanceID)
	}

	instanceRunning := false
	createdSSHKey := false
	for i := 0; i < 50 && !instanceRunning; i++ {
		if i > 0 {
			time.Sleep(time.Second * 20)
		}

		var op *ec2.DescribeInstancesOutput
		op, err = a.ec2Service.DescribeInstances(context.TODO(), &ec2.DescribeInstancesInput{
			InstanceIds: []string{testInstance.instanceID},
		})
		if err != nil {
			continue
		}
		instance := op.Reservations[0].Instances[0]
		if instance.State.Name != types.InstanceStateNameRunning {
			continue
		}

		if len(instance.NetworkInterfaces) == 0 {
			klog.Infof("instance %s does not have network interfaces yet", testInstance.instanceID)
			continue
		}
		sourceDestCheck := instance.NetworkInterfaces[0].SourceDestCheck
		if sourceDestCheck != nil && *sourceDestCheck == true {
			networkInterfaceID := instance.NetworkInterfaces[0].NetworkInterfaceId
			modifyInput := &ec2.ModifyNetworkInterfaceAttributeInput{
				NetworkInterfaceId: networkInterfaceID,
				SourceDestCheck:    &types.AttributeBooleanValue{Value: aws.Bool(false)},
			}
			_, err = a.ec2Service.ModifyNetworkInterfaceAttribute(context.TODO(), modifyInput)
			if err != nil {
				klog.Infof("unable to set SourceDestCheck on instance %s", testInstance.instanceID)
			}
		}

		testInstance.publicIP = *instance.PublicIpAddress

		// generate a temporary SSH key and send it to the node via instance-connect
		if *instanceConnect && !createdSSHKey {
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
			err = fmt.Errorf("instance %s not running containerd/crio daemon: %s", testInstance.instanceID, output)
			continue
		}

		instanceRunning = true
	}

	if !instanceRunning {
		return nil, fmt.Errorf("instance %s is not running, %w", testInstance.instanceID, err)
	}
	return testInstance, nil
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
	_, err = a.ec2icService.SendSSHPublicKey(context.TODO(),
		&ec2instanceconnect.SendSSHPublicKeyInput{
			InstanceId:       aws.String(testInstance.instanceID),
			InstanceOSUser:   aws.String(remote.GetSSHUser()),
			SSHPublicKey:     aws.String(string(key.public)),
			AvailabilityZone: testInstance.instance.Placement.AvailabilityZone,
		})
	if err != nil {
		return fmt.Errorf("sending SSH public key for serial console access, %w", err)
	}
	client, err := ssh.Dial("tcp", fmt.Sprintf("%s:22", testInstance.publicIP), &ssh.ClientConfig{
		User:            remote.GetSSHUser(),
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(key.signer),
		},
	})
	if err != nil {
		return fmt.Errorf("dialing SSH %s@%s %w", remote.GetSSHUser(), testInstance.publicIP, err)
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

func (a *AWSRunner) launchNewInstance(img internalAWSImage) (*types.Instance, error) {
	images, err := a.ec2Service.DescribeImages(context.TODO(),
		&ec2.DescribeImagesInput{ImageIds: []string{img.amiID}})
	if err != nil {
		return nil, fmt.Errorf("describing images, %w", err)
	}

	input := &ec2.RunInstancesInput{
		InstanceType: types.InstanceType(img.instanceType),
		ImageId:      &img.amiID,
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		NetworkInterfaces: []types.InstanceNetworkInterfaceSpecification{
			{
				AssociatePublicIpAddress: aws.Bool(true),
				DeviceIndex:              aws.Int32(0),
			},
		},
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeInstance,
				Tags: []types.Tag{
					{
						Key:   aws.String("Name"),
						Value: aws.String(a.cfg.InstanceNamePrefix + img.imageDesc),
					},
					// tagged so we can find it easily
					{
						Key:   aws.String(amiIDTag),
						Value: aws.String(img.amiID),
					},
				},
			},
			{
				ResourceType: types.ResourceTypeVolume,
				Tags: []types.Tag{
					{
						Key:   aws.String("Name"),
						Value: aws.String(a.cfg.InstanceNamePrefix + img.imageDesc),
					},
				},
			},
		},
		BlockDeviceMappings: []types.BlockDeviceMapping{
			{
				DeviceName: aws.String(*images.Images[0].RootDeviceName),
				Ebs: &types.EbsBlockDevice{
					VolumeSize: aws.Int32(50),
					VolumeType: "gp3",
				},
			},
		},
	}
	if len(img.userData) > 0 {
		input.UserData = aws.String(base64.StdEncoding.EncodeToString(img.userData))
	}
	if img.instanceProfile != "" {
		input.IamInstanceProfile = &types.IamInstanceProfileSpecification{
			Name: &img.instanceProfile,
		}
	}

	rsv, err := a.ec2Service.RunInstances(context.TODO(), input)
	if err != nil {
		return nil, fmt.Errorf("creating instance, %w", err)
	}

	return &rsv.Instances[0], nil
}

func (a *AWSRunner) getSSMImage(path string) (string, error) {
	rsp, err := a.ssmService.GetParameter(context.TODO(), &ssm.GetParameterInput{
		Name: &path,
	})
	if err != nil {
		return "", fmt.Errorf("getting AMI ID from SSM path %q, %w", path, err)
	}
	return *rsp.Parameter.Value, nil
}

type awsInstance struct {
	instance         *types.Instance
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

func readUserdata(userdataFile string) ([]byte, error) {
	userdata, err := os.ReadFile(userdataFile)
	if err != nil {
		return nil, fmt.Errorf("reading userdata file %q, %w", userdataFile, err)
	}
	return userdata, nil
}
