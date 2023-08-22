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

package utils

import (
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/iam"

	"k8s.io/klog/v2"
)

func EnsureRole(svc *iam.IAM, roleName string) error {
	listRolesInput := &iam.ListRolesInput{
		PathPrefix: aws.String("/kubetest2/"),
	}

	listRolesResult, err := svc.ListRoles(listRolesInput)
	if err != nil {
		return err
	}
	if len(listRolesResult.Roles) > 0 {
		for _, role := range listRolesResult.Roles {
			if *role.RoleName == roleName {
				klog.Infof("%s role exists already ARN: %s\n", roleName, *role.Arn)
				return nil
			}
		}
	} else {
		klog.Infof("did not find any pre-existing %s. creating %s...\n", roleName, roleName)
	}

	rolePolicyJSON := map[string]interface{}{
		"Version": "2012-10-17",
		"Statement": []map[string]interface{}{
			{
				"Effect": "Allow",
				"Principal": map[string]interface{}{
					"Service": "eks.amazonaws.com",
				},
				"Action": "sts:AssumeRole",
			},
			{
				"Effect": "Allow",
				"Principal": map[string]interface{}{
					"Service": "ec2.amazonaws.com",
				},
				"Action": "sts:AssumeRole",
			},
		},
	}
	rolePolicy, err := json.Marshal(rolePolicyJSON)
	if err != nil {
		return err
	}

	createRoleInput := iam.CreateRoleInput{
		RoleName:                 aws.String(roleName),
		Path:                     aws.String("/kubetest2/"),
		AssumeRolePolicyDocument: aws.String(string(rolePolicy)),
	}
	result, err := svc.CreateRole(&createRoleInput)
	if err != nil {
		return err
	}
	klog.Infof("create role succeeded ARN : %v\n", *result.Role.Arn)

	policies := []string{
		"arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly",
		"arn:aws:iam::aws:policy/AmazonEKSClusterPolicy",
		"arn:aws:iam::aws:policy/AmazonEKSServicePolicy",
		"arn:aws:iam::aws:policy/AmazonEKSVPCResourceController",
		"arn:aws:iam::aws:policy/AmazonEKSWorkerNodePolicy",
		"arn:aws:iam::aws:policy/AmazonEKS_CNI_Policy",
		"arn:aws:iam::aws:policy/AmazonS3ReadOnlyAccess",
	}

	for _, policy := range policies {
		_, err = svc.AttachRolePolicy(&iam.AttachRolePolicyInput{
			PolicyArn: aws.String(policy),
			RoleName:  aws.String(roleName),
		})
		if err != nil {
			return fmt.Errorf("failed to attach policy : %w", err)
		}
	}
	return nil
}
