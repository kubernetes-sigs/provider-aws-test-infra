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

package build

import (
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
)

type Options struct {
	StageLocation   string `flag:"~stage" desc:"Upload binaries to s3 bucket if set"`
	RepoRoot        string `flag:"-"`
	VersionSuffix   string `flag:"-"`
	TargetBuildArch string `flag:"~target-build-arch" desc:"Target architecture for the test artifacts"`
	S3Uploader      *s3manager.Uploader
	Builder
	Stager
}

func (o *Options) Validate() error {
	return o.implementationFromStrategy()
}

func (o *Options) implementationFromStrategy() error {
	o.Builder = &MakeBuilder{
		RepoRoot:        o.RepoRoot,
		TargetBuildArch: o.TargetBuildArch,
	}
	o.Stager = &S3Stager{
		RepoRoot:        o.RepoRoot,
		StageLocation:   o.StageLocation,
		s3Uploader:      o.S3Uploader,
		TargetBuildArch: o.TargetBuildArch,
	}
	return nil
}
