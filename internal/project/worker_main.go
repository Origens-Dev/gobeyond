package project

import (
	"fmt"
	"path"
)

func generatedWorkerMain(websiteImport string, worker Worker) ([]byte, error) {
	workerImport := path.Join(websiteImport, GeneratedDir, "workers", worker.Key)
	source := fmt.Sprintf(`%s
package main

import (
	"context"
	"log"
	"os"

	gb "github.com/Origens-Dev/gobeyond"
	gbtemporal "github.com/Origens-Dev/gobeyond/adapters/temporal"
	workerpkg %q
)

func main() {
	if os.Getenv("GOBEYOND_TEMPORAL_TASK_QUEUE") == "" {
		queue, err := gb.TaskQueueName(%q, gb.LocalEnvironment)
		if err != nil {
			log.Fatal(err)
		}
		_ = os.Setenv("GOBEYOND_TEMPORAL_TASK_QUEUE", queue)
	}
	if os.Getenv("GOBEYOND_TEMPORAL_NAMESPACE") == "" {
		_ = os.Setenv("GOBEYOND_TEMPORAL_NAMESPACE", "default")
	}
	if err := gbtemporal.Serve(context.Background(), gbtemporal.Options{
		Register: workerpkg.Register,
	}); err != nil {
		log.Fatal(err)
	}
}
`, generatedSourceMarker+"\n", workerImport, worker.ID)
	return []byte(source), nil
}
