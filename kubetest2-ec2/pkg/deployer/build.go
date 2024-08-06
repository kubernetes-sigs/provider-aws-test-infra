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

package deployer

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	awsv2 "github.com/aws/aws-sdk-go-v2/aws"
	s3managerv2 "github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	s3v2 "github.com/aws/aws-sdk-go-v2/service/s3"

	"k8s.io/klog/v2"

	"sigs.k8s.io/provider-aws-test-infra/kubetest2-ec2/pkg/deployer/build"
)

func (d *deployer) Build() error {
	klog.Info("EC2 deployer starting Build()")

	runner := d.NewAWSRunner()
	_, err := runner.InitializeServices()
	if err != nil {
		return fmt.Errorf("unable to initialize AWS services : %w", err)
	}

	s3Uploader := s3managerv2.NewUploader(d.runner.s3Service, func(u *s3managerv2.Uploader) {
		u.PartSize = 10 * 1024 * 1024 // 10 MB
		u.Concurrency = 10
	})

	d.BuildOptions.CommonBuildOptions.S3Service = d.runner.s3Service
	d.BuildOptions.CommonBuildOptions.S3Uploader = s3Uploader
	d.BuildOptions.CommonBuildOptions.RepoRoot = d.RepoRoot

	err = d.BuildOptions.Validate()
	if err != nil {
		return err
	}

	// this supports the kubernetes/kubernetes build
	klog.Info("starting to build kubernetes")
	version, err := d.BuildOptions.Build()
	if err != nil {
		return err
	}

	// stage build if requested
	bucket := d.BuildOptions.CommonBuildOptions.StageLocation
	if d.BuildOptions.CommonBuildOptions.StageLocation != "" {
		if strings.Contains(d.BuildOptions.CommonBuildOptions.StageLocation, "://") {
			return fmt.Errorf("unsupported stage location, please specify the name of the s3 bucket (without s3:// prefix)")
		}
		_, err := d.runner.s3Service.HeadBucket(context.TODO(), &s3v2.HeadBucketInput{Bucket: awsv2.String(bucket)})
		if err != nil {
			return fmt.Errorf("unable to find bucket %q, %v", bucket, err)
		}
		if err := d.BuildOptions.Stage(version); err != nil {
			return fmt.Errorf("error staging build: %v", err)
		}
		klog.Infof("staged version %s to s3 bucket %s", version, bucket)
	}
	build.StoreCommonBinaries(d.RepoRoot, d.commonOptions.RunDir(),
		runtime.GOOS+"/"+runtime.GOARCH)
	return nil
}
