/*
Copyright 2026.

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

package admin

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	rsh "github.com/openstack-k8s-operators/lib-common/modules/common/rsh"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// ContainerName is the container in a broker pod that runs RabbitMQ, and so
// the one the CLI has to be run in.
const ContainerName = "rabbitmq"

// commandTimeout limits one CLI command. The commands run here either read
// cluster state or change membership, and a broker that has stopped answering
// should not hold a reconcile open indefinitely.
const commandTimeout = 2 * time.Minute

// ExecFunc runs a command in a container and hands back what it wrote to
// stdout and stderr. It exists as a type so that tests can supply their own
// without a Kubernetes API server.
type ExecFunc func(
	ctx context.Context,
	client kubernetes.Interface,
	config *rest.Config,
	podName types.NamespacedName,
	container string,
	command []string,
	callback func(*bytes.Buffer, *bytes.Buffer) error,
) error

// WithExec lets the client run CLI commands in broker pods.
//
// Not every caller needs this. Reading cluster state over the management API
// requires nothing but credentials, while changing membership requires running
// commands inside a pod, which in turn requires the operator to hold
// create permission on pods/exec. A client built without this can still do
// everything in client.go and cluster.go.
//
// Pass nil for exec to use the ordinary in-pod executor.
func (c *Client) WithExec(kclient kubernetes.Interface, config *rest.Config, exec ExecFunc) *Client {
	c.kclient = kclient
	c.config = config
	c.exec = exec
	return c
}

// CommandOutput is what a CLI command wrote. Both streams are kept, because a
// failing RabbitMQ command puts its explanation on either one depending on the
// command.
type CommandOutput struct {
	Stdout string
	Stderr string
}

// Command runs a RabbitMQ CLI command in a broker pod.
//
// On failure the error carries whatever the command printed, truncated, since
// that text is usually the only explanation of what went wrong.
func (c *Client) Command(
	ctx context.Context,
	podName types.NamespacedName,
	command string,
	args ...string,
) (CommandOutput, error) {
	// A caller-supplied executor does not need a Kubernetes client; the
	// default one does. Having neither means there is no way to run anything.
	if c.exec == nil && c.kclient == nil {
		return CommandOutput{}, fmt.Errorf("RabbitMq admin client cannot run commands: built without exec")
	}
	cmd := append([]string{command}, args...)
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()

	executor := c.exec
	if executor == nil {
		executor = rsh.ExecInPod
	}
	var output CommandOutput
	err := executor(commandCtx, c.kclient, c.config, podName, ContainerName, cmd,
		func(out *bytes.Buffer, errOut *bytes.Buffer) error {
			output.Stdout = out.String()
			output.Stderr = errOut.String()
			return nil
		})
	if err != nil {
		detail := strings.TrimSpace(output.Stdout + "\n" + output.Stderr)
		if detail != "" {
			return output, fmt.Errorf("%w (output: %.4096s)", err, detail)
		}
	}
	return output, err
}

// Ctl runs rabbitmqctl in a broker pod and returns its stdout.
func (c *Client) Ctl(ctx context.Context, podName types.NamespacedName, args ...string) (string, error) {
	output, err := c.Command(ctx, podName, "rabbitmqctl", args...)
	return output.Stdout, err
}

// ForgetNode removes a node from the cluster, run against a surviving broker.
// The node being removed does not take part, which is the point: it is used
// when that broker is gone and cannot be asked to leave on its own.
func (c *Client) ForgetNode(ctx context.Context, peer *corev1.Pod, node string) error {
	_, err := c.Ctl(ctx, podKey(peer), "forget_cluster_node", node)
	return err
}

func podKey(pod *corev1.Pod) types.NamespacedName {
	return types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}
}
