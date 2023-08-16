package deployer

import (
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/klog/v2"

	"sigs.k8s.io/provider-aws-test-infra/kubetest2-ec2/pkg/deployer/remote"
)

func (d *deployer) DumpClusterLogs() error {
	klog.Infof("copying logs to %s", d.logsDir)
	_, err := os.Stat(d.logsDir)
	if os.IsNotExist(err) {
		err := os.Mkdir(d.logsDir, os.ModePerm)
		if err != nil {
			return fmt.Errorf("failed to create %s: %s", d.logsDir, err)
		}
		klog.Infof("created logs directory %s", d.logsDir)
	} else {
		if err != nil {
			return fmt.Errorf("unexpected exception when making cluster logs directory: %s", err)
		}
	}

	d.dumpContainerdInstallationLogs()
	d.dumpContainerdLogs()
	d.dumpCloudInitLogs()
	d.dumpKubeletLogs()
	d.kubectlDump()
	d.dumpVPCCNILogs()

	return nil
}

func (d *deployer) dumpContainerdInstallationLogs() {
	d.dumpRemoteLogs("containerd-installation", "journalctl", "-u", "containerd-installation", "--no-pager")
}

func (d *deployer) dumpContainerdLogs() {
	d.dumpRemoteLogs("containerd", "journalctl", "-u", "containerd", "--no-pager")
}

func (d *deployer) dumpKubeletLogs() {
	d.dumpRemoteLogs("kubelet", "journalctl", "-u", "kubelet", "--no-pager")
}

func (d *deployer) dumpCloudInitLogs() {
	d.dumpRemoteLogs("cloud-init-output", "cat", "/var/log/cloud-init-output.log")
}

func (d *deployer) kubectlDump() {
	d.dumpRemoteLogs("cluster-info",
		"kubectl",
		"--kubeconfig",
		"/etc/kubernetes/admin.conf",
		"cluster-info",
		"dump")
}

func (d *deployer) dumpRemoteLogs(outputFilePrefix string, args ...string) {
	for _, instance := range d.runner.instances {
		file := outputFilePrefix + ".log"
		klog.Infof("Running command to dump logs to file %s/%s: %v", instance.instanceID, file, args)
		output, err := remote.SSH(instance.instanceID, args...)
		if err != nil {
			klog.Errorf("error running %v - Command failed: %s", args, instance.instanceID, output)
		}
		outfile, err := os.Create(filepath.Join(d.logsDir, instance.instanceID, file))
		if err != nil {
			klog.Errorf("failed to create %s log files : %w", outputFilePrefix, err)
		} else {
			defer outfile.Close()
		}
		_, err = outfile.WriteString(output)
		if err != nil {
			klog.Errorf("failed to write to %s log file: %w", outputFilePrefix, err)
		}
	}
}

func (d *deployer) dumpVPCCNILogs() {
	for _, instance := range d.runner.instances {
		destDir := filepath.Join(d.logsDir, instance.instanceID, "aws-cni")
		err := os.MkdirAll(destDir, os.ModePerm)
		if err != nil {
			klog.Errorf("failed to create %s: %s", destDir, err)
			continue
		}
		output, err := remote.SSH(instance.instanceID, "/opt/cni/bin/aws-cni-support.sh")
		if err != nil {
			klog.Errorf("error running /opt/cni/bin/aws-cni-support.sh - Command failed: %s",
				instance.instanceID, output)
		}
		output, err = remote.SCP(instance.instanceID, "/var/log/eks*.tar.gz", destDir)
		if err != nil {
			klog.Errorf("error scp from /var/log/eks*.tar.gz failed: %s", instance.instanceID, output)
		}
	}
}
