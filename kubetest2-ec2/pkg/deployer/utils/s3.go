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
	"context"
	"fmt"
	"strings"

	"k8s.io/klog/v2"

	awsv2 "github.com/aws/aws-sdk-go-v2/aws"
	s3v2 "github.com/aws/aws-sdk-go-v2/service/s3"

	"golang.org/x/exp/maps"
)

func ValidateS3Bucket(s3Service *s3v2.Client, stageLocation string, version string) error {
	if strings.Contains(stageLocation, "://") {
		return nil
	}

	results, err := s3Service.ListObjectsV2(context.TODO(), &s3v2.ListObjectsV2Input{
		Bucket: awsv2.String(stageLocation),
		Prefix: awsv2.String(version),
	})
	if err != nil {
		return fmt.Errorf("version %s is missing from bucket %s: %w",
			version,
			stageLocation,
			err)
	} else if results.KeyCount == nil || *results.KeyCount == 0 {
		results, err = s3Service.ListObjectsV2(context.TODO(), &s3v2.ListObjectsV2Input{
			Bucket: awsv2.String(stageLocation),
			Prefix: awsv2.String("v"),
		})

		if err != nil {
			return fmt.Errorf("unable to list items in bucket %s: %w",
				stageLocation,
				err)
		}

		availableVersions := map[string]string{}
		if results != nil && results.KeyCount != nil && *results.KeyCount > 0 {
			count := 0
			for _, item := range results.Contents {
				dir := strings.Split(*item.Key, "/")[0]
				if _, ok := availableVersions[dir]; !ok {
					klog.Infof("Found version %s in bucket %s", dir, stageLocation)
					availableVersions[dir] = *item.Key
					klog.Infof("checking if key %s is a prefix of %s", dir, version)
					if len(version) > len(dir) && strings.HasPrefix(version, dir) {
						count++
						klog.Infof("bucket %s has %s for use with %s", stageLocation, dir, version)
					}
				}
			}
			if count > 0 {
				return nil
			}
		}

		return fmt.Errorf("version %s is missing from bucket %s, choose one of %s",
			version,
			stageLocation,
			maps.Keys(availableVersions))
	}
	klog.Infof("found requested version %s in bucket %s", version, stageLocation)
	return nil
}
