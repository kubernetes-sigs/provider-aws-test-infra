package utils

import (
	"context"
	"encoding/base64"
	"fmt"
	awsv2 "github.com/aws/aws-sdk-go-v2/aws"
	ec2v2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2typesv2 "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	iamv2 "github.com/aws/aws-sdk-go-v2/service/iam"
	"math/rand"
	"strings"
	"time"

	"k8s.io/klog/v2"

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

func LaunchNewInstance(ec2Service *ec2v2.Client, iamService *iamv2.Client,
	clusterID string, controlPlaneIP string, img InternalAWSImage, subnetID string) (*ec2typesv2.Instance, error) {
	images, err := ec2Service.DescribeImages(context.TODO(), &ec2v2.DescribeImagesInput{ImageIds: []string{img.AmiID}})
	if err != nil {
		return nil, fmt.Errorf("describing images: %w", err)
	}

	name := clusterID + uuid.New().String()[:8]
	input := &ec2v2.RunInstancesInput{
		InstanceType: ec2typesv2.InstanceType(img.InstanceType),
		ImageId:      &img.AmiID,
		MinCount:     awsv2.Int32(1),
		MaxCount:     awsv2.Int32(1),
		MetadataOptions: &ec2typesv2.InstanceMetadataOptionsRequest{
			HttpEndpoint: "enabled",
			HttpTokens:   "required",
		},
		NetworkInterfaces: []ec2typesv2.InstanceNetworkInterfaceSpecification{
			{
				SubnetId:                 awsv2.String(subnetID),
				AssociatePublicIpAddress: awsv2.Bool(true),
				DeviceIndex:              awsv2.Int32(0),
			},
		},
		TagSpecifications: []ec2typesv2.TagSpecification{
			{
				ResourceType: ec2typesv2.ResourceTypeInstance,
				Tags: []ec2typesv2.Tag{
					{
						Key:   awsv2.String("Name"),
						Value: awsv2.String(name),
					},
					{
						Key:   awsv2.String("kubernetes.io/cluster/" + clusterID),
						Value: awsv2.String("owned"),
					},
				},
			},
			{
				ResourceType: ec2typesv2.ResourceTypeVolume,
				Tags: []ec2typesv2.Tag{
					{
						Key:   awsv2.String("Name"),
						Value: awsv2.String(name),
					},
				},
			},
		},
		BlockDeviceMappings: []ec2typesv2.BlockDeviceMapping{
			{
				DeviceName: awsv2.String(*images.Images[0].RootDeviceName),
				Ebs: &ec2typesv2.EbsBlockDevice{
					VolumeSize: awsv2.Int32(50),
					VolumeType: "gp3",
				},
			},
		},
	}
	if len(img.UserData) > 0 {
		data := strings.ReplaceAll(img.UserData, "{{KUBEADM_CONTROL_PLANE_IP}}", controlPlaneIP)
		input.UserData = awsv2.String(base64.StdEncoding.EncodeToString([]byte(data)))
	}
	if img.InstanceProfile != "" {
		arn, err := GetInstanceProfileArn(iamService, img.InstanceProfile)
		if err != nil {
			return nil, fmt.Errorf("getting instance profile arn, %w", err)
		}
		input.IamInstanceProfile = &ec2typesv2.IamInstanceProfileSpecification{
			Arn: awsv2.String(arn),
		}
	}

	rsv, err := ec2Service.RunInstances(context.TODO(), input)
	if err != nil {
		return nil, fmt.Errorf("creating instance, %w", err)
	}

	return WaitForInstanceToRun(ec2Service, &rsv.Instances[0]), nil
}

func WaitForInstanceToRun(ec2Service *ec2v2.Client, instance *ec2typesv2.Instance) *ec2typesv2.Instance {
	for i := 0; i < 30; i++ {
		if i > 0 {
			time.Sleep(time.Second * 5)
		}

		op, err := ec2Service.DescribeInstances(context.TODO(), &ec2v2.DescribeInstancesInput{
			InstanceIds: []string{*instance.InstanceId},
		})
		if err != nil {
			continue
		}
		instance = &op.Reservations[0].Instances[0]
		if instance.State.Name == ec2typesv2.InstanceStateNameRunning {
			break
		}
	}
	return instance
}

func PickSubnetID(svc *ec2v2.Client) (string, string, error) {
	defaultVpcID, err := getDefaultVPC(svc)
	if err != nil {
		return "", "", fmt.Errorf("Failed to get default VPC: %v", err)
	}
	klog.Infof("Default VPC ID: %s\n", defaultVpcID)

	// Get subnet IDs for the default VPC
	subnetIDs, err := getSubnetIDs(svc, defaultVpcID)
	if err != nil {
		return "", "", fmt.Errorf("Failed to get subnet IDs: %v", err)
	}

	// Print the results
	klog.Infof("Subnet IDs: %v", subnetIDs)
	if len(subnetIDs) == 0 {
		return "", "", fmt.Errorf("No subnets found in the default VPC: %s", defaultVpcID)
	}
	randomSubnetID := subnetIDs[rand.Intn(len(subnetIDs))]
	klog.Infof("Randomly picked subnet ID: %s\n", randomSubnetID)
	return randomSubnetID, defaultVpcID, nil
}

func getDefaultVPC(svc *ec2v2.Client) (string, error) {
	input := &ec2v2.DescribeVpcsInput{
		Filters: []ec2typesv2.Filter{
			{
				Name:   awsv2.String("isDefault"),
				Values: []string{"true"},
			},
		},
	}

	result, err := svc.DescribeVpcs(context.TODO(), input)
	if err != nil {
		return "", err
	}

	if len(result.Vpcs) == 0 {
		return "", fmt.Errorf("no default VPC found")
	}

	return *result.Vpcs[0].VpcId, nil
}

func getSubnetIDs(svc *ec2v2.Client, vpcID string) ([]string, error) {
	input := &ec2v2.DescribeSubnetsInput{
		Filters: []ec2typesv2.Filter{
			{
				Name:   awsv2.String("vpc-id"),
				Values: []string{vpcID},
			},
		},
	}

	result, err := svc.DescribeSubnets(context.TODO(), input)
	if err != nil {
		return nil, err
	}

	var subnetIDs []string
	for _, subnet := range result.Subnets {
		// skip known AZ where instance types we need are not available
		if *subnet.AvailabilityZone == "us-east-1e" {
			continue
		}
		subnetIDs = append(subnetIDs, *subnet.SubnetId)
	}

	return subnetIDs, nil
}
