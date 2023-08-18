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
	"fmt"
	"runtime"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"

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

	s3Uploader := s3manager.NewUploaderWithClient(d.runner.s3Service, func(u *s3manager.Uploader) {
		u.PartSize = 10 * 1024 * 1024 // 50 mb
		u.Concurrency = 10
	})
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
		_, err := d.runner.s3Service.HeadBucket(&s3.HeadBucketInput{Bucket: aws.String(bucket)})
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
