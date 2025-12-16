# kubetest2-ec2

[Kubetest2](https://github.com/kubernetes-sigs/kubetest2/blob/master/README.md#kubetest2) is a framework for 
deploying Kubernetes clusters and running end-to-end tests against them.

`kubetest2-ec2` is a deployer for `kubetest2` to deploy a `kubeadm` based cluster on EC2 nodes. `kubetest2-ec2`
manages the lifecycle of a temporary Kubernetes cluster for testing. `kubetest2-ec2` uses ubuntu images to install
and run the kubernetes cluster.

## Installation

To install kubetest2-ec2:
`go install sigs.k8s.io/provider-aws-test-infra/kubetest2-ec2@latest`

You will need `kubetest2` itself and `kubetest2-ginkgo` (a tester) to be able to run a test.

## Usage

General usage is of the form:
```
kubetest2 <deployer> [Flags] [DeployerFlags] -- [TesterArgs]
```

**Example**: list all flags for the `ec2` deployer and `ginkgo` tester
```
kubetest2 ec2 --test=ginkgo --help
```

**Example**: deploy a cluster using a local checkout of `kubernetes/kubernetes`, run Conformance tests

```bash
kubetest2 ec2 \
 --repo-root $HOME/go/src/k8s.io/kubernetes \
 --region us-east-1 \
 --target-build-arch linux/amd64 \
 --stage provider-aws-test-infra \
 --build \
 --up \
 --down \
 --test=ginkgo \
 -- \
 --focus-regex='\[Conformance\]'
```

if `--build` is not specified then `kubetest2` skips that phase. if `--down` is not specified, `kubetest2` leaves the
cluster up and running for further inspection. `--test` is need to actually run the test. you can use either
`--focus-regex` to run specific tests and/or `--skip-regex` to skip some.

Here is a slightly different example:

```bash
kubetest2 ec2 \
 --region=us-east-1 \
 --stage provider-aws-test-infra \
 --target-build-arch linux/arm64 \
  --build \
 --up \
 --test=ginkgo \
 -- \
 --use-built-binaries true \
 --skip-regex='\[Driver:.gcepd\]|\[Slow\]|\[Serial\]|\[Disruptive\]|\[Flaky\]|\[Feature:.+\]' \
 --ginkgo-parallel=30
```

Here's the simplest example that builds and stands up a cluster:

```bash
kubetest2 ec2 \
 --stage dims-aws-test-infra \
 --build \
 --up
```

Instead of building, pushing to s3 buckets and then standing up a cluster from there, you can use release
artifacts directly as well, like so:
```bash
kubetest2 ec2 \
  --stage https://dl.k8s.io/ \
  --version v1.28.0 \
  --up
```

Here's an example of how to start latest tip of kubernetes master with a AL2023 worker image: 
```bash
kubetest2 ec2 \
 --stage https://dl.k8s.io/ci/fast/ \
 --version $(curl -Ls https://dl.k8s.io/ci/fast/latest-fast.txt) \
 --region us-east-1 \
 --target-build-arch linux/amd64 \
 --worker-image al2023 \
 --up
```

So you can see that a lot of things have defaults and/or picked up from the environment (like the AWS credentials)

Some important CLI parameters are:

| Parameter                 | Example                            | Use                                                                                          |
|---------------------------|------------------------------------|----------------------------------------------------------------------------------------------|
| `stage`                   | `--stage provider-aws-test-infra`  | s3 bucket where the `--build` process uploads artifacts to and `--up` process downloads from |
| `instance-type`           | `--instance-type=m6a.large`        | specify an EC2 instance type                                                                 |
| `images`                  | `--images=ami-02675d30b814d1daa`   | specify a custom ubuntu image, uses SSM to pick up new ubuntu LTS images if not specified    |
| `region`                  | `--region us-east-1`               | specify a AWS region, defaults to `us-east-1`                                                |
| `target-build-arch`       | `--target-build-arch linux/amd64`  | supports both `linux/amd64` and `linux/arm64`                                                |
| `external-cloud-provider` | `--external-cloud-provider true`   | to use AWS External cloud provider when starting the nodes and the cluster                   |

## CNI Options

The deployer uses the following CNI plugins:

1. **Cilium CNI** (default): Provides better network reliability, observability, and debugging
   capabilities. Uses VXLAN tunneling for reliable pod-to-pod communication across nodes.
   This is used by default for all clusters without external cloud provider.

2. **AWS VPC CNI** (with `--external-cloud-provider true`): Native AWS networking using ENIs.
   Best performance and integrates with AWS services. Used automatically when external cloud
   provider is enabled.

## Test Parallelism

The default test parallelism for node e2e tests has been reduced to 4 (from 8) to avoid network
and resource contention issues. You can override this by setting the `PARALLELISM` environment variable:

```bash
PARALLELISM=8 hack/make-rules/test-e2e-node.sh
```


## Reference Implementations

See individual READMEs for more information

### Contact

Learn how to engage with the Kubernetes community on the [community page](http://kubernetes.io/community/).

You can reach the maintainers of this project at:

- [Slack](https://kubernetes.slack.com/messages/sig-testing)
- [Mailing List](https://groups.google.com/forum/#!forum/kubernetes-sig-testing)

### Code of conduct

Participation in the Kubernetes community is governed by the [Kubernetes Code of Conduct](https://kubernetes.io/community/code-of-conduct/).

<!-- links -->
[kubetest]: https://git.k8s.io/test-infra/kubetest
