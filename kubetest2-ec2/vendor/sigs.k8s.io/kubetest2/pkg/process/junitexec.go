/*
Copyright 2019 The Kubernetes Authors.

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

package process

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"

	"sigs.k8s.io/kubetest2/pkg/metadata"
)

type execJunitError struct {
	error
	systemout string
}

func (e *execJunitError) SystemOut() string {
	return e.systemout
}

var _ metadata.JUnitError = &execJunitError{}

// ExecJUnit is like Exec, except that it tees the output and captures it
// for returning a metadata.JUnitError if the process does not exit success
func ExecJUnit(argv0 string, args []string, env []string) error {
	// construct command from inputs
	cmd := exec.Command(argv0, args...)
	return execJUnit(cmd, env)
}

func ExecJUnitContext(ctx context.Context, argv0 string, args []string, env []string) error {
	cmd := exec.CommandContext(ctx, argv0, args...)
	return execJUnit(cmd, env)
}

func execJUnit(cmd *exec.Cmd, env []string) error {
	cmd.Env = env

	// inherit some standard file descriptors, as if `syscall.Exec`ed
	cmd.Stdin = os.Stdin
	// ensure we also capture output
	var systemout bytes.Buffer
	syncSystemOut := &mutexWriter{
		writer: &systemout,
	}
	cmd.Stdout = io.MultiWriter(syncSystemOut, os.Stdout)
	cmd.Stderr = io.MultiWriter(syncSystemOut, os.Stderr)

	// actually execute, return a JUnit error if the command errors
	if err := execCmdWithSignals(cmd); err != nil {
		return &execJunitError{
			error:     err,
			systemout: systemout.String(),
		}
	}
	return nil
}
