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
	"slices"
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
	clusterID string, controlPlaneIP string, img InternalAWSImage, subnetID string, ipFamily string) (*ec2typesv2.Instance, error) {
	images, err := ec2Service.DescribeImages(context.TODO(), &ec2v2.DescribeImagesInput{ImageIds: []string{img.AmiID}})
	if err != nil {
		return nil, fmt.Errorf("describing images: %w", err)
	}

	netIface := ec2typesv2.InstanceNetworkInterfaceSpecification{
		SubnetId:                 awsv2.String(subnetID),
		AssociatePublicIpAddress: awsv2.Bool(true),
		DeviceIndex:              awsv2.Int32(0),
	}
	if ipFamily == "ipv6" || ipFamily == "dual" {
		netIface.Ipv6AddressCount = awsv2.Int32(1)
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
		NetworkInterfaces: []ec2typesv2.InstanceNetworkInterfaceSpecification{netIface},
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
		compressed, err := GzipBytes([]byte(data))
		if err != nil {
			return nil, fmt.Errorf("compressing user data, %w", err)
		}
		input.UserData = awsv2.String(base64.StdEncoding.EncodeToString(compressed))
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

// IsCapacityError reports whether RunInstances failed for a reason that is
// specific to the subnet or its availability zone, so the same launch can
// succeed in another subnet.
func IsCapacityError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "InsufficientInstanceCapacity") ||
		strings.Contains(msg, "InsufficientFreeAddressesInSubnet")
}

// EnsureSSHSelfIngress allows TCP 22 between instances that share the VPC's
// default security group (instances launch without an explicit group, so they
// all land in it). The e2e framework reaches worker nodes by tunneling SSH
// through the control plane (KUBE_SSH_BASTION); that second hop targets a
// node's private IP and only a security group rule can admit it. A rule that
// already exists returns InvalidPermission.Duplicate, which counts as success.
func EnsureSSHSelfIngress(svc *ec2v2.Client, vpcID string) error {
	out, err := svc.DescribeSecurityGroups(context.TODO(), &ec2v2.DescribeSecurityGroupsInput{
		Filters: []ec2typesv2.Filter{
			{Name: awsv2.String("vpc-id"), Values: []string{vpcID}},
			{Name: awsv2.String("group-name"), Values: []string{"default"}},
		},
	})
	if err != nil {
		return fmt.Errorf("describing default security group of vpc %s: %w", vpcID, err)
	}
	if len(out.SecurityGroups) == 0 {
		return fmt.Errorf("no default security group in vpc %s", vpcID)
	}
	groupID := out.SecurityGroups[0].GroupId
	_, err = svc.AuthorizeSecurityGroupIngress(context.TODO(), &ec2v2.AuthorizeSecurityGroupIngressInput{
		GroupId: groupID,
		IpPermissions: []ec2typesv2.IpPermission{{
			IpProtocol:       awsv2.String("tcp"),
			FromPort:         awsv2.Int32(22),
			ToPort:           awsv2.Int32(22),
			UserIdGroupPairs: []ec2typesv2.UserIdGroupPair{{GroupId: groupID}},
		}},
	})
	if err != nil {
		if strings.Contains(err.Error(), "InvalidPermission.Duplicate") {
			return nil
		}
		return fmt.Errorf("authorizing ssh ingress on security group %s: %w", *groupID, err)
	}
	klog.Infof("allowed ssh (tcp/22) between instances in security group %s", *groupID)
	return nil
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

func PickSubnetID(svc *ec2v2.Client, ipFamily string, exclude ...string) (string, string, error) {
	defaultVpcID, err := getDefaultVPC(svc)
	if err != nil {
		return "", "", fmt.Errorf("Failed to get default VPC: %v", err)
	}
	klog.Infof("Default VPC ID: %s\n", defaultVpcID)

	// Get subnet IDs for the default VPC. When the caller asked for an IPv6
	// or dual-stack cluster, restrict to subnets that already have an IPv6
	// CIDR association — instance launch would otherwise fail with
	// InvalidParameterValue: ipv6AddressCount cannot be specified on a subnet
	// without IPv6 enabled.
	requireIPv6 := ipFamily == "ipv6" || ipFamily == "dual"
	subnetIDs, err := getSubnetIDs(svc, defaultVpcID, requireIPv6, exclude)
	if err != nil {
		return "", "", fmt.Errorf("Failed to get subnet IDs: %v", err)
	}

	// Print the results
	klog.Infof("Subnet IDs (ipFamily=%q, requireIPv6=%v): %v", ipFamily, requireIPv6, subnetIDs)
	if len(subnetIDs) == 0 {
		if requireIPv6 {
			return "", "", fmt.Errorf("No IPv6-enabled subnets found in the default VPC %s; enable an IPv6 CIDR on at least one subnet or run without --ip-family=%s", defaultVpcID, ipFamily)
		}
		if len(exclude) > 0 {
			return "", "", fmt.Errorf("No subnet other than %v is usable in the default VPC %s", exclude, defaultVpcID)
		}
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

// minFreeIPsPerSubnet is the number of free IPv4 addresses a subnet needs
// before a cluster is launched into it. A default /20 subnet has 4091 and a
// three-node cluster takes about 150 at boot (one primary address plus three
// /28 prefixes per node for the VPC CNI). A subnet far below that is shared
// with something else and the VPC CNI finds no free aligned /28 in it
// (InsufficientCidrBlocks), so pods never get an IP.
const minFreeIPsPerSubnet = 1024

func getSubnetIDs(svc *ec2v2.Client, vpcID string, requireIPv6 bool, exclude []string) ([]string, error) {
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

	var subnetIDs, lowOnIPs []string
	for _, subnet := range result.Subnets {
		id, az := *subnet.SubnetId, *subnet.AvailabilityZone
		// skip known AZ where instance types we need are not available
		if az == "us-east-1e" || slices.Contains(exclude, id) {
			continue
		}
		if requireIPv6 && !hasIPv6CIDR(subnet) {
			continue
		}
		free := awsv2.ToInt32(subnet.AvailableIpAddressCount)
		if free < minFreeIPsPerSubnet {
			klog.Warningf("skipping subnet %s (%s): only %d free IPv4 addresses", id, az, free)
			lowOnIPs = append(lowOnIPs, id)
			continue
		}
		klog.Infof("subnet %s (%s): %d free IPv4 addresses", id, az, free)
		subnetIDs = append(subnetIDs, id)
	}
	if len(subnetIDs) == 0 && len(lowOnIPs) > 0 {
		klog.Warningf("every subnet in VPC %s has fewer than %d free IPv4 addresses, picking from %v anyway", vpcID, minFreeIPsPerSubnet, lowOnIPs)
		return lowOnIPs, nil
	}
	return subnetIDs, nil
}

// hasIPv6CIDR returns true when the subnet has at least one associated IPv6
// CIDR block that is in the associated state (i.e. usable by instances).
func hasIPv6CIDR(subnet ec2typesv2.Subnet) bool {
	for _, assoc := range subnet.Ipv6CidrBlockAssociationSet {
		if assoc.Ipv6CidrBlockState != nil &&
			assoc.Ipv6CidrBlockState.State == ec2typesv2.SubnetCidrBlockStateCodeAssociated {
			return true
		}
	}
	return false
}
