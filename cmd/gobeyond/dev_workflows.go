package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	gb "github.com/Origens-Dev/gobeyond"
	"github.com/Origens-Dev/gobeyond/buildpaths"
)

const devWorkflowRetryDelay = 2 * time.Second

type devWorkflowSupervisor struct {
	ctx              context.Context
	root             string
	environment      []string
	queueEnvironment string

	mu         sync.Mutex
	cancel     context.CancelFunc
	generation *sync.WaitGroup
}

func newDevWorkflowSupervisor(ctx context.Context, root string, environment []string, queueEnvironment string) *devWorkflowSupervisor {
	if strings.TrimSpace(queueEnvironment) == "" {
		queueEnvironment = gb.LocalEnvironment
	}
	return &devWorkflowSupervisor{ctx: ctx, root: root, environment: environment, queueEnvironment: queueEnvironment}
}

func (supervisor *devWorkflowSupervisor) replace(buildDirectory string) {
	workers, err := discoverDevWorkflowWorkers(buildDirectory)
	if err != nil {
		fmt.Fprintln(os.Stderr, "GoBeyond local workflow discovery:", err)
		return
	}

	supervisor.mu.Lock()
	previousCancel := supervisor.cancel
	previousGeneration := supervisor.generation
	generationContext, cancel := context.WithCancel(supervisor.ctx)
	generation := &sync.WaitGroup{}
	// Set the count before publishing generation. close can otherwise observe
	// this WaitGroup between the assignment below and Add in the launch loop,
	// which is an unsafe Add/Wait race and can leave a worker behind on reload.
	generation.Add(len(workers))
	supervisor.cancel = cancel
	supervisor.generation = generation
	supervisor.mu.Unlock()

	if previousCancel != nil {
		previousCancel()
	}
	if previousGeneration != nil {
		previousGeneration.Wait()
	}
	if len(workers) == 0 {
		fmt.Println("GoBeyond found no local workflow queues")
		return
	}
	for _, worker := range workers {
		worker := worker
		go func() {
			defer generation.Done()
			supervisor.runWorker(generationContext, worker)
		}()
	}
}

func (supervisor *devWorkflowSupervisor) close() {
	supervisor.mu.Lock()
	cancel := supervisor.cancel
	generation := supervisor.generation
	supervisor.cancel = nil
	supervisor.generation = nil
	supervisor.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if generation != nil {
		generation.Wait()
	}
}

type devWorkflowWorker struct {
	queue  string
	binary string
}

func discoverDevWorkflowWorkers(buildDirectory string) ([]devWorkflowWorker, error) {
	root := filepath.Join(buildDirectory, buildpaths.WorkersDir)
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var workers []devWorkflowWorker
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		binary := filepath.Join(root, entry.Name(), buildpaths.WorkerEntryName)
		info, statErr := os.Stat(binary)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return nil, statErr
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("workflow queue binary %s is not a regular file", binary)
		}
		workers = append(workers, devWorkflowWorker{queue: entry.Name(), binary: binary})
	}
	sort.Slice(workers, func(i, j int) bool { return workers[i].queue < workers[j].queue })
	return workers, nil
}

func (supervisor *devWorkflowSupervisor) runWorker(ctx context.Context, worker devWorkflowWorker) {
	for {
		err := runDevWorkflowAttempt(ctx, supervisor.root, supervisor.environment, supervisor.queueEnvironment, worker)
		if ctx.Err() != nil {
			return
		}
		fmt.Fprintf(os.Stderr, "GoBeyond workflow queue %s unavailable; retrying in %s: %v\n", worker.queue, devWorkflowRetryDelay, err)
		timer := time.NewTimer(devWorkflowRetryDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

func runDevWorkflowAttempt(ctx context.Context, root string, environment []string, queueEnvironment string, worker devWorkflowWorker) error {
	physicalQueue, err := gb.TaskQueueName(worker.queue, queueEnvironment)
	if err != nil {
		return err
	}
	readinessDirectory, err := os.MkdirTemp("/tmp", "gobeyond-workflow-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(readinessDirectory)
	readinessSocket := filepath.Join(readinessDirectory, "ready.sock")
	address, err := net.ResolveUnixAddr("unixgram", readinessSocket)
	if err != nil {
		return err
	}
	listener, err := net.ListenUnixgram("unixgram", address)
	if err != nil {
		return err
	}
	defer listener.Close()

	nonce := fmt.Sprintf("%s-%d", worker.queue, time.Now().UnixNano())
	command := exec.Command(worker.binary)
	command.Dir = root
	command.Env = withEnvironment(environment,
		"GOBEYOND_TEMPORAL_TASK_QUEUE="+physicalQueue,
		"GOBEYOND_TEMPORAL_ENVIRONMENT="+queueEnvironment,
		"GOBEYOND_READINESS_NONCE="+nonce,
		"GOBEYOND_READINESS_SIGNAL=unixgram://"+readinessSocket,
	)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err := command.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	ready := make(chan error, 1)
	go func() {
		if err := listener.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
			ready <- err
			return
		}
		buffer := make([]byte, 4096)
		count, _, err := listener.ReadFromUnix(buffer)
		if err != nil {
			ready <- err
			return
		}
		if strings.TrimSpace(string(buffer[:count])) != nonce {
			ready <- errors.New("workflow readiness nonce did not match")
			return
		}
		ready <- nil
	}()

	select {
	case <-ctx.Done():
		stopDevWorkflowProcess(command, done)
		return ctx.Err()
	case err := <-done:
		if err == nil {
			return errors.New("workflow worker exited before readiness")
		}
		return err
	case err := <-ready:
		if err != nil {
			stopDevWorkflowProcess(command, done)
			return fmt.Errorf("wait for workflow readiness: %w", err)
		}
	}

	fmt.Printf("GoBeyond workflow queue %s ready (%s)\n", worker.queue, physicalQueue)
	select {
	case <-ctx.Done():
		stopDevWorkflowProcess(command, done)
		return ctx.Err()
	case err := <-done:
		if err == nil {
			return errors.New("workflow worker exited")
		}
		return err
	}
}

func stopDevWorkflowProcess(command *exec.Cmd, done <-chan error) {
	if command.Process == nil {
		return
	}
	_ = command.Process.Signal(os.Interrupt)
	select {
	case <-done:
		return
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		<-done
	}
}
