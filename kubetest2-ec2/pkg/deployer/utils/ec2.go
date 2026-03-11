package utils

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand"
	"net"
	"strings"
	"time"

	awsv2 "github.com/aws/aws-sdk-go-v2/aws"
	ec2v2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2typesv2 "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	iamv2 "github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/smithy-go"

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
			func() ec2typesv2.InstanceNetworkInterfaceSpecification {
				iface := ec2typesv2.InstanceNetworkInterfaceSpecification{
					SubnetId:                 awsv2.String(subnetID),
					AssociatePublicIpAddress: awsv2.Bool(true),
					DeviceIndex:              awsv2.Int32(0),
				}
				if ipFamily != "" && ipFamily != "ipv4" {
					iface.Ipv6AddressCount = awsv2.Int32(1)
				}
				return iface
			}(),
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

// EnsureIPv6 associates an Amazon-provided IPv6 CIDR with the given VPC (idempotent),
// then associates a /64 block with the subnet and enables IPv6 auto-assignment.
// It also adds a ::/0 route via the VPC's Internet Gateway to the subnet's route table.
func EnsureIPv6(ctx context.Context, svc *ec2v2.Client, vpcID, subnetID string) (string, error) {
	// 1. Check if VPC already has an IPv6 CIDR
	vpcsOut, err := svc.DescribeVpcs(ctx, &ec2v2.DescribeVpcsInput{VpcIds: []string{vpcID}})
	if err != nil {
		return "", fmt.Errorf("describe VPC %s: %w", vpcID, err)
	}
	var vpcIPv6CIDR string
	for _, a := range vpcsOut.Vpcs[0].Ipv6CidrBlockAssociationSet {
		if a.Ipv6CidrBlockState.State == ec2typesv2.VpcCidrBlockStateCodeAssociated {
			vpcIPv6CIDR = awsv2.ToString(a.Ipv6CidrBlock)
			break
		}
	}

	// 2. If not, request one from Amazon's pool and wait for it to become active
	if vpcIPv6CIDR == "" {
		out, err := svc.AssociateVpcCidrBlock(ctx, &ec2v2.AssociateVpcCidrBlockInput{
			VpcId:                       awsv2.String(vpcID),
			AmazonProvidedIpv6CidrBlock: awsv2.Bool(true),
		})
		if err != nil {
			return "", fmt.Errorf("associate IPv6 CIDR with VPC %s: %w", vpcID, err)
		}

		assocID := awsv2.ToString(out.Ipv6CidrBlockAssociation.AssociationId)

		err = waitForIPv6Association(ctx, svc, func(ctx context.Context, svc *ec2v2.Client) error {
			klog.Infof("checking VPC %s for IPv6 association", vpcID)
			out, err := svc.DescribeVpcs(ctx, &ec2v2.DescribeVpcsInput{VpcIds: []string{vpcID}})
			if err != nil {
				return err
			}
			for _, a := range out.Vpcs[0].Ipv6CidrBlockAssociationSet {
				if a.Ipv6CidrBlock == nil {
					return fmt.Errorf("IPv6 CIDR not yet associated with VPC")
				}
				if awsv2.ToString(a.AssociationId) == assocID {
					if a.Ipv6CidrBlockState.State == ec2typesv2.VpcCidrBlockStateCodeAssociated {
						klog.Infof("found vpcIPv6CIDR: %s", a.Ipv6CidrBlock)
						vpcIPv6CIDR = awsv2.ToString(a.Ipv6CidrBlock)
						return nil
					}
				}
			}
			return fmt.Errorf("timed out waiting for VPC %s IPv6 CIDR association %s", vpcID, assocID)
		})

		if err != nil {
			return "", err
		}
	}

	// 3. Carve a /64 for the subnet (if not already present) and enable auto-assignment
	var hasIPv6 bool
	var subnetOut *ec2v2.DescribeSubnetsOutput
	checkSubnetAssociated := func(ctx context.Context, svc *ec2v2.Client) error {
		klog.Infof("checking subnet %s for IPv6 CIDR", subnetID)
		subnetOut, err = svc.DescribeSubnets(ctx, &ec2v2.DescribeSubnetsInput{SubnetIds: []string{subnetID}})
		if err != nil {
			return fmt.Errorf("describe subnet %s: %w", subnetID, err)
		}
		klog.Infof("subnetOut items: %d", len(subnetOut.Subnets))
		klog.Infof("association set items: %d", len(subnetOut.Subnets[0].Ipv6CidrBlockAssociationSet))
		hasIPv6 = false
		for _, a := range subnetOut.Subnets[0].Ipv6CidrBlockAssociationSet {
			klog.Infof("found the subnet")
			if a.Ipv6CidrBlockState.State == ec2typesv2.SubnetCidrBlockStateCodeAssociated {
				klog.Infof("CIDR associated")
				hasIPv6 = true
				break
			}
		}
		return nil
	}

	// Check the subnet's association initially to determine if it has IPv6 yet.
	checkSubnetAssociated(ctx, svc)
	klog.Infof("subnetOut items after check: %d", len(subnetOut.Subnets))

	if !hasIPv6 {
		subnetIPv6CIDR, err := ipv6SubnetCIDR(
			awsv2.ToString(subnetOut.Subnets[0].CidrBlock),
			vpcIPv6CIDR,
		)
		if err != nil {
			return "", fmt.Errorf("derive /64 for subnet %s: %w", subnetID, err)
		}
		if _, err := svc.AssociateSubnetCidrBlock(ctx, &ec2v2.AssociateSubnetCidrBlockInput{
			SubnetId:      awsv2.String(subnetID),
			Ipv6CidrBlock: awsv2.String(subnetIPv6CIDR),
		}); err != nil {
			return "", fmt.Errorf("associate IPv6 CIDR %s with subnet %s: %w", subnetIPv6CIDR, subnetID, err)
		}
		err = waitForIPv6Association(ctx, svc, checkSubnetAssociated)
		if err != nil {
			return "", err
		}
	}

	if _, err := svc.ModifySubnetAttribute(ctx, &ec2v2.ModifySubnetAttributeInput{
		SubnetId:                    awsv2.String(subnetID),
		AssignIpv6AddressOnCreation: &ec2typesv2.AttributeBooleanValue{Value: awsv2.Bool(true)},
	}); err != nil {
		return "", fmt.Errorf("enable IPv6 auto-assignment on subnet %s: %w", subnetID, err)
	}

	// 4. Add ::/0 route via the IGW to the subnet's route table (if not already present)
	igwOut, err := svc.DescribeInternetGateways(ctx, &ec2v2.DescribeInternetGatewaysInput{
		Filters: []ec2typesv2.Filter{
			{Name: awsv2.String("attachment.vpc-id"), Values: []string{vpcID}},
		},
	})
	if err != nil || len(igwOut.InternetGateways) == 0 {
		return "", fmt.Errorf("find internet gateway for VPC %s: %w", vpcID, err)
	}
	igwID := awsv2.ToString(igwOut.InternetGateways[0].InternetGatewayId)

	// Prefer the route table explicitly associated with the subnet; fall back to the VPC main table.
	rtOut, err := svc.DescribeRouteTables(ctx, &ec2v2.DescribeRouteTablesInput{
		Filters: []ec2typesv2.Filter{
			{Name: awsv2.String("association.subnet-id"), Values: []string{subnetID}},
		},
	})
	if err != nil {
		return "", fmt.Errorf("describe route tables for subnet %s: %w", subnetID, err)
	}
	if len(rtOut.RouteTables) == 0 {
		rtOut, err = svc.DescribeRouteTables(ctx, &ec2v2.DescribeRouteTablesInput{
			Filters: []ec2typesv2.Filter{
				{Name: awsv2.String("vpc-id"), Values: []string{vpcID}},
				{Name: awsv2.String("association.main"), Values: []string{"true"}},
			},
		})
		if err != nil || len(rtOut.RouteTables) == 0 {
			return "", fmt.Errorf("find main route table for VPC %s: %w", vpcID, err)
		}
	}
	rtID := awsv2.ToString(rtOut.RouteTables[0].RouteTableId)
	hasIPv6Route := false
	for _, r := range rtOut.RouteTables[0].Routes {
		if awsv2.ToString(r.DestinationIpv6CidrBlock) == "::/0" {
			hasIPv6Route = true
			break
		}
	}
	if !hasIPv6Route {
		if _, err := svc.CreateRoute(ctx, &ec2v2.CreateRouteInput{
			RouteTableId:             awsv2.String(rtID),
			DestinationIpv6CidrBlock: awsv2.String("::/0"),
			GatewayId:                awsv2.String(igwID),
		}); err != nil {
			return "", fmt.Errorf("create ::/0 route in route table %s: %w", rtID, err)
		}
	}

	return vpcIPv6CIDR, nil
}

// waitFunc defines a function that will execute an AWS Client call and return an error.
// It will be executed in a loop until a) it returns `nil` or b) timeout is passed.
type waitFunc func(ctx context.Context, svc *ec2v2.Client) error

func waitForIPv6Association(ctx context.Context, svc *ec2v2.Client, waiterFunc waitFunc) error {
	var err error
	var i int

	for i = 0; i < 30; i++ {
		err = waiterFunc(ctx, svc)
		if err == nil {
			break
		}
		time.Sleep(5 * time.Second)
	}

	if i == 30 {
		err = fmt.Errorf("timed out waiting for condition")
	}

	return err
}

// ipv6SubnetCIDR derives a /64 prefix for the given subnet from the VPC's /56 IPv6 block.
// The index byte is an FNV hash of the subnet's IPv4 CIDR string. Collisions are
// theoretically possible but negligible for AWS VPCs (≤200 subnets per VPC).
func ipv6SubnetCIDR(subnetIPv4CIDR, vpcIPv6CIDR string) (string, error) {
	h := fnv.New32a()
	h.Write([]byte(subnetIPv4CIDR))

	klog.Infof("Trying to use net.ParseCIDR")

	_, vpcIPv6Net, err := net.ParseCIDR(vpcIPv6CIDR)
	if err != nil {
		return "", fmt.Errorf("parse VPC IPv6 CIDR %s: %w", vpcIPv6CIDR, err)
	}
	ip6 := make(net.IP, 16)
	copy(ip6, vpcIPv6Net.IP)
	ip6[7] = byte(h.Sum32())
	for i := 8; i < 16; i++ {
		ip6[i] = 0
	}
	return fmt.Sprintf("%s/64", ip6), nil
}

// TeardownIPv6Subnet disassociates all IPv6 /64 CIDR blocks from the subnet.
// The subnet deletion itself and the VPC-level /56 are handled separately.
func TeardownIPv6Subnet(ctx context.Context, svc *ec2v2.Client, subnetID string) error {
	out, err := svc.DescribeSubnets(ctx, &ec2v2.DescribeSubnetsInput{SubnetIds: []string{subnetID}})
	if err != nil {
		return fmt.Errorf("describe subnet %s: %w", subnetID, err)
	}
	if len(out.Subnets) == 0 {
		return nil // already deleted
	}

	// AWS's API needs a *bool, not just a bool.
	falseVal := false

	// Remove the IPv6 auto-assignment before disassociating the IPv6 CIDR.
	modifyInput := &ec2v2.ModifySubnetAttributeInput{
		SubnetId: &subnetID,
		AssignIpv6AddressOnCreation: &ec2typesv2.AttributeBooleanValue{
			Value: &falseVal,
		},
	}

	_, err = svc.ModifySubnetAttribute(ctx, modifyInput)
	if err != nil {
		return fmt.Errorf("could not disable ipv6 creation on assignment for subnet %s: %w", subnetID, err)
	}

	err = waitForIPv6Association(ctx, svc, func(ctx context.Context, svc *ec2v2.Client) error {
		for _, a := range out.Subnets[0].Ipv6CidrBlockAssociationSet {
			if a.Ipv6CidrBlockState.State == ec2typesv2.SubnetCidrBlockStateCodeAssociated {
				if _, err := svc.DisassociateSubnetCidrBlock(ctx, &ec2v2.DisassociateSubnetCidrBlockInput{
					AssociationId: a.AssociationId,
				}); err != nil {
					var smithyErr smithy.APIError
					// resource may be in use, keep trying if so.
					if errors.As(err, &smithyErr) && smithyErr.ErrorCode() == "400" {
						continue
					}
				}
				// no error
				return nil
			}
		}
		return fmt.Errorf("disassociate IPv6 CIDR from subnet %s: %w", subnetID, err)
	})

	if err != nil {
		return fmt.Errorf("timed out waiting for IPv6 CIDR to disassoiate from subnet %s: %w", subnetID, err)
	}

	return nil
}
