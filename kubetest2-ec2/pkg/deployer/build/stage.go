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
	"bufio"
	"os"
	"strings"

	"k8s.io/klog/v2"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
)

type Stager interface {
	// Stage determines how kubernetes artifacts will be staged (e.g. to say a GCS bucket)
	// for the specified version
	Stage(version string) error
}

type NoopStager struct{}

var _ Stager = &NoopStager{}

func (n *NoopStager) Stage(string) error {
	return nil
}

type S3Stager struct {
	StageLocation   string
	s3Uploader      *s3manager.Uploader
	TargetBuildArch string
	RepoRoot        string
}

var _ Stager = &S3Stager{}

func (n *S3Stager) Stage(version string) error {
	tgzFile := "kubernetes-server-" + strings.ReplaceAll(n.TargetBuildArch, "/", "-") + ".tar.gz"
	destinationKey := aws.String(version + "/" + tgzFile)
	klog.Infof("uploading %s to s3://%s/%s", tgzFile, n.StageLocation, *destinationKey)

	f, err := os.Open(n.RepoRoot + "/_output/release-tars/" + tgzFile)
	if err != nil {
		return err
	}
	defer f.Close()

	reader := bufio.NewReader(f)

	// Upload the file to S3.
	input := &s3manager.UploadInput{
		Bucket: aws.String(n.StageLocation),
		Key:    destinationKey,
		Body:   reader,
	}
	_, err = n.s3Uploader.Upload(input)

	return err
}
