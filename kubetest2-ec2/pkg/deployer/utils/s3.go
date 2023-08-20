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
	"fmt"
	"strings"

	"golang.org/x/exp/maps"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
)

func ValidateS3Bucket(s3Service *s3.S3, stageLocation string, stageVersion string, version string) error {
	if strings.Contains(stageLocation, "://") {
		return nil
	}

	results, err := s3Service.ListObjectsV2(&s3.ListObjectsV2Input{
		Bucket: aws.String(stageLocation),
		Prefix: aws.String(version),
	})
	if err != nil {
		return fmt.Errorf("version %s is missing from bucket %s: %w",
			stageVersion,
			stageLocation,
			err)
	} else if results.KeyCount == nil || *results.KeyCount == 0 {
		results, _ = s3Service.ListObjectsV2(&s3.ListObjectsV2Input{
			Bucket: aws.String(stageLocation),
			Prefix: aws.String("v"),
		})

		availableVersions := map[string]string{}
		if results != nil && results.KeyCount != nil && *results.KeyCount > 0 {
			for _, item := range results.Contents {
				dir := strings.Split(*item.Key, "/")[0]
				if _, ok := availableVersions[dir]; !ok {
					availableVersions[dir] = *item.Key
				}
			}
		}

		return fmt.Errorf("version %s is missing from bucket %s, choose one of %s",
			stageVersion,
			stageLocation,
			maps.Keys(availableVersions))
	}
	return nil
}
