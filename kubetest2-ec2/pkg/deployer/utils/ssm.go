package utils

import (
	"fmt"
	"github.com/aws/aws-sdk-go/service/ssm"
)

func GetSSMImage(ssmService *ssm.SSM, path string) (string, error) {
	rsp, err := ssmService.GetParameter(&ssm.GetParameterInput{
		Name: &path,
	})
	if err != nil {
		return "", fmt.Errorf("getting AMI ID from SSM path %q, %w", path, err)
	}
	return *rsp.Parameter.Value, nil
}
