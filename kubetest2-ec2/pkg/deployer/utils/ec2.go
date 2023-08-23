package utils

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/iam"
	"github.com/google/uuid"
)

type InternalAWSImage struct {
	AmiID string
	// The instance type (e.g. t3a.medium)
	InstanceType string
	UserData     string
	ImageDesc    string
	// name of the instance profile
	InstanceProfile string
}

func LaunchNewInstance(ec2Service *ec2.EC2, iamService *iam.IAM,
	clusterID string, controlPlaneIP string, img InternalAWSImage) (*ec2.Instance, error) {
	images, err := ec2Service.DescribeImages(&ec2.DescribeImagesInput{ImageIds: []*string{&img.AmiID}})
	if err != nil {
		return nil, fmt.Errorf("describing images: %w in region (%s)", err, *ec2Service.Config.Region)
	}

	name := clusterID + uuid.New().String()[:8]
	input := &ec2.RunInstancesInput{
		InstanceType: &img.InstanceType,
		ImageId:      &img.AmiID,
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
						Value: aws.String(name),
					},
					{
						Key:   aws.String("kubernetes.io/cluster/" + clusterID),
						Value: aws.String("owned"),
					},
				},
			},
			{
				ResourceType: aws.String(ec2.ResourceTypeVolume),
				Tags: []*ec2.Tag{
					{
						Key:   aws.String("Name"),
						Value: aws.String(name),
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
	if len(img.UserData) > 0 {
		data := strings.ReplaceAll(img.UserData, "{{KUBEADM_CONTROL_PLANE_IP}}", controlPlaneIP)
		input.UserData = aws.String(base64.StdEncoding.EncodeToString([]byte(data)))
	}
	if img.InstanceProfile != "" {
		arn, err := GetInstanceProfileArn(iamService, img.InstanceProfile)
		if err != nil {
			return nil, fmt.Errorf("getting instance profile arn, %w", err)
		}
		input.IamInstanceProfile = &ec2.IamInstanceProfileSpecification{
			Arn: aws.String(arn),
		}
	}

	rsv, err := ec2Service.RunInstances(input)
	if err != nil {
		return nil, fmt.Errorf("creating instance, %w", err)
	}

	return WaitForInstanceToRun(ec2Service, rsv.Instances[0]), nil
}

func WaitForInstanceToRun(ec2Service *ec2.EC2, instance *ec2.Instance) *ec2.Instance {
	for i := 0; i < 30; i++ {
		if i > 0 {
			time.Sleep(time.Second * 5)
		}

		op, err := ec2Service.DescribeInstances(&ec2.DescribeInstancesInput{
			InstanceIds: []*string{instance.InstanceId},
		})
		if err != nil {
			continue
		}
		instance = op.Reservations[0].Instances[0]
		if *instance.State.Name == ec2.InstanceStateNameRunning {
			break
		}
	}
	return instance
}
