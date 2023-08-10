package main

import (
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/iam"

	"sigs.k8s.io/provider-aws-test-infra/kubetest2-ec2/pkg/deployer/utils"
)

func main() {
	sess, _ := session.NewSession()
	svc := iam.New(sess, &aws.Config{Region: aws.String("us-east-1")})
	err := utils.EnsureRole(svc, "provider-aws-test-role")
	if err != nil {
		fmt.Printf("error with ensure role: %v\n", err)
	}
	err = utils.EnsureInstanceProfile(svc, "provider-aws-test-instance-profile", "provider-aws-test-role")
	if err != nil {
		fmt.Printf("error with ensure instance profile: %v\n", err)
	}
}
