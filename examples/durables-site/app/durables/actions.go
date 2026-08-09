// Package durables implements actions declared by the /durables route.
package durables

import (
	"fmt"
	"os"
	"strings"
	"time"

	gb "github.com/Origens-Dev/gobeyond"
	startDemo "github.com/Origens-Dev/gobeyond/examples/durables-site/generated/contracts/actions/r_durables_259a06a8_start_demo"
	echoOnce "github.com/Origens-Dev/gobeyond/examples/durables-site/generated/contracts/actions/r_durables_259a06a8_start_echo_once"
	"go.temporal.io/sdk/client"
)

func StartEchoOnce(ctx *gb.ActionContext, _ echoOnce.Input) (echoOnce.Output, error) {
	workflowID, err := startWorkflow(ctx, "default.echo-once", "hello from durables-site")
	if err != nil {
		return echoOnce.Output{}, err
	}
	return echoOnce.Output{Started: true, WorkflowID: workflowID}, nil
}

func StartDemo(ctx *gb.ActionContext, _ startDemo.Input) (startDemo.Output, error) {
	workflowID, err := startWorkflow(ctx, "default.demo", "demo from durables-site")
	if err != nil {
		return startDemo.Output{}, err
	}
	return startDemo.Output{Started: true, WorkflowID: workflowID}, nil
}

func startWorkflow(ctx *gb.ActionContext, workflowName, message string) (string, error) {
	queue, err := gb.TaskQueueName(gb.DefaultTaskQueueID, gb.LocalEnvironment)
	if err != nil {
		return "", err
	}
	address := strings.TrimSpace(os.Getenv("GOBEYOND_TEMPORAL_ADDRESS"))
	if address == "" {
		address = "localhost:7233"
	}
	namespace := strings.TrimSpace(os.Getenv("GOBEYOND_TEMPORAL_NAMESPACE"))
	if namespace == "" {
		namespace = "default"
	}

	c, err := client.Dial(client.Options{
		HostPort:  address,
		Namespace: namespace,
	})
	if err != nil {
		return "", fmt.Errorf("dial Temporal at %s (start compose in examples/durables-site): %w", address, err)
	}
	defer c.Close()

	workflowID := fmt.Sprintf("%s-%d", workflowName, time.Now().UnixNano())
	_, err = c.ExecuteWorkflow(ctx.Context, client.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: queue,
	}, workflowName, message)
	if err != nil {
		return "", err
	}
	return workflowID, nil
}
