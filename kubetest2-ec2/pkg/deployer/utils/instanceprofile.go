package utils

import (
	"context"
	"fmt"
	"k8s.io/klog/v2"

	awsv2 "github.com/aws/aws-sdk-go-v2/aws"
	iamv2 "github.com/aws/aws-sdk-go-v2/service/iam"
)

func GetInstanceProfileArn(svc *iamv2.Client, instanceProfileName string) (string, error) {
	listInstanceProfilesInput := &iamv2.ListInstanceProfilesInput{
		PathPrefix: awsv2.String("/kubetest2/"),
	}
	listInstanceProfilesResult, err := svc.ListInstanceProfiles(context.TODO(), listInstanceProfilesInput)
	if err != nil {
		return "", err
	}
	if len(listInstanceProfilesResult.InstanceProfiles) > 0 {
		for _, profile := range listInstanceProfilesResult.InstanceProfiles {
			if *profile.InstanceProfileName == instanceProfileName {
				return *profile.Arn, nil
			}
		}
	}
	return "", fmt.Errorf("unable to find Arn for %s instance profile", instanceProfileName)
}

func EnsureInstanceProfile(svc *iamv2.Client, instanceProfileName string, roleName string) error {

	listInstanceProfilesInput := &iamv2.ListInstanceProfilesInput{
		PathPrefix: awsv2.String("/kubetest2/"),
	}

	listInstanceProfilesResult, err := svc.ListInstanceProfiles(context.TODO(), listInstanceProfilesInput)
	if err != nil {
		return err
	}
	if len(listInstanceProfilesResult.InstanceProfiles) > 0 {
		for _, profile := range listInstanceProfilesResult.InstanceProfiles {
			if *profile.InstanceProfileName == instanceProfileName {
				klog.Infof("%s instance profile exists already ARN: %s\n", instanceProfileName, *profile.Arn)
				return nil
			}
		}
	} else {
		klog.Infof("did not find any pre-existing %s. creating %s...\n", instanceProfileName, instanceProfileName)
	}

	createInput := &iamv2.CreateInstanceProfileInput{
		InstanceProfileName: awsv2.String(instanceProfileName),
		Path:                awsv2.String("/kubetest2/"),
	}

	createResult, err := svc.CreateInstanceProfile(context.TODO(), createInput)
	if err != nil {
		return fmt.Errorf("unable to create instance profile : %w", err)
	}
	klog.Infof("created instance profile: %v\n", *createResult.InstanceProfile.Arn)

	listProfilesForRoleInput := &iamv2.ListInstanceProfilesForRoleInput{RoleName: awsv2.String(roleName)}
	listProfilesForRoleResult, err := svc.ListInstanceProfilesForRole(context.TODO(), listProfilesForRoleInput)
	if err != nil {
		return fmt.Errorf("unable to list instance profile for role: %w", err)
	}
	if len(listProfilesForRoleResult.InstanceProfiles) > 0 {
		klog.Infof("found instance profile %s for role %s already", instanceProfileName, roleName)
		return nil
	}

	addInput := &iamv2.AddRoleToInstanceProfileInput{
		InstanceProfileName: awsv2.String(instanceProfileName),
		RoleName:            awsv2.String(roleName),
	}
	_, err = svc.AddRoleToInstanceProfile(context.TODO(), addInput)
	if err != nil {
		return fmt.Errorf("unable to add role to instance profile : %w", err)
	}
	klog.Infof("added role %s to instance profile %s\n", roleName, instanceProfileName)
	return nil
}
