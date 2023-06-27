# Sync this file contents with https://github.com/kubernetes/kubernetes/blob/master/build/root/Makefile
DBG_MAKEFILE ?=
ifeq ($(DBG_MAKEFILE),1)
    $(warning ***** starting Makefile for goal(s) "$(MAKECMDGOALS)")
    $(warning ***** $(shell date))
else
    # If we're not debugging the Makefile, don't echo recipes.
    MAKEFLAGS += -s
endif


# Old-skool build tools.
#
# Commonly used targets (see each target for more information):
#   all: Build code.
#   test: Run tests.
#   clean: Clean up.

# It's necessary to set this because some environments don't link sh -> bash.
SHELL := /usr/bin/env bash -o errexit -o pipefail -o nounset

# Define variables so `make --warn-undefined-variables` works.
PRINT_HELP ?=

# We don't need make's built-in rules.
MAKEFLAGS += --no-builtin-rules
.SUFFIXES:

define TEST_E2E_NODE_HELP_INFO
# Build and run node end-to-end tests.
#
# Args:
#  FOCUS: Regexp that matches the tests to be run.  Defaults to "".
#  SKIP: Regexp that matches the tests that needs to be skipped.
#    Defaults to "\[Flaky\]|\[Slow\]|\[Serial\]".
#  TEST_ARGS: A space-separated list of arguments to pass to node e2e test.
#    Defaults to "".
#  RUN_UNTIL_FAILURE: If true, pass --untilItFails to ginkgo so tests are run
#    repeatedly until they fail.  Defaults to false.
#  REMOTE: If true, run the tests on a remote host instance on GCE.  Defaults
#    to false.
#  ARTIFACTS: Local directory to scp test artifacts into from the remote hosts
#    for REMOTE=true. Local directory to write juntil xml results into for REMOTE=false.
#    Defaults to "/tmp/_artifacts/$$(date +%y%m%dT%H%M%S)".
#  LIST_IMAGES: For REMOTE=true only. If true, don't run tests. Just output the
#    list of available images for testing.  Defaults to false.
#  TIMEOUT: For REMOTE=true only. How long (in golang duration format) to wait
#    for ginkgo tests to complete. Defaults to 45m.
#  PARALLELISM: The number of ginkgo nodes to run.  Defaults to 8.
#  RUNTIME: Container runtime to use (eg. docker, remote).
#    Defaults to "docker".
#  CONTAINER_RUNTIME_ENDPOINT: remote container endpoint to connect to.
#    Used when RUNTIME is set to "remote".
#  IMAGE_SERVICE_ENDPOINT: remote image endpoint to connect to, to prepull images.
#    Used when RUNTIME is set to "remote".
#  IMAGE_CONFIG_FILE: path to a file containing image configuration.
#  SYSTEM_SPEC_NAME: The name of the system spec to be used for validating the
#    image in the node conformance test. The specs are located at
#    test/e2e_node/system/specs/. For example, "SYSTEM_SPEC_NAME=gke" will use
#    the spec at test/e2e_node/system/specs/gke.yaml. If unspecified, the
#    default built-in spec (system.DefaultSpec) will be used.
#  IMAGES: For REMOTE=true only.  Comma delimited list of images for creating
#    remote hosts to run tests against.  Defaults to a recent image.
#  HOSTS: For REMOTE=true only.  Comma delimited list of running gce hosts to
#    run tests against.  Defaults to "".
#  DELETE_INSTANCES: For REMOTE=true only.  Delete any instances created as
#    part of this test run.  Defaults to false.
#  PREEMPTIBLE_INSTANCES: For REMOTE=true only.  Mark created gce instances
#    as preemptible.  Defaults to false.
#  CLEANUP: For REMOTE=true only.  If false, do not stop processes or delete
#    test files on remote hosts.  Defaults to true.
#  IMAGE_PROJECT: For REMOTE=true only.  Project containing images provided to
#    $$IMAGES.  Defaults to "kubernetes-node-e2e-images".
#  INSTANCE_PREFIX: For REMOTE=true only.  Instances created from images will
#    have the name "$${INSTANCE_PREFIX}-$${IMAGE_NAME}".  Defaults to "test".
#  INSTANCE_METADATA: For REMOTE=true and running on GCE only.
#  GUBERNATOR: For REMOTE=true only. Produce link to Gubernator to view logs.
#    Defaults to false.
#  TEST_SUITE: For REMOTE=true only. Test suite to use. Defaults to "default".
#
# Example:
#   make test-e2e-node FOCUS=Kubelet SKIP=container
#   make test-e2e-node REMOTE=true DELETE_INSTANCES=true
#   make test-e2e-node TEST_ARGS='--kubelet-flags="--cgroups-per-qos=true"'
# Build and run tests.
endef
.PHONY: test-e2e-node
ifeq ($(PRINT_HELP),y)
test-e2e-node:
	@echo "$$TEST_E2E_NODE_HELP_INFO"
else
test-e2e-node:
	hack/make-rules/test-e2e-node.sh
endif
