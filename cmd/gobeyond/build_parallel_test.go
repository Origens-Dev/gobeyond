package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRunBuildTasksRunsTasksConcurrently(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		done <- runBuildTasks(
			buildTask{name: "first", run: func() error {
				started <- "first"
				<-release
				return nil
			}},
			buildTask{name: "second", run: func() error {
				started <- "second"
				<-release
				return nil
			}},
		)
	}()

	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case name := <-started:
			seen[name] = true
		case <-time.After(2 * time.Second):
			t.Fatal("build tasks did not start concurrently")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunBuildTasksReturnsErrorsInDeclarationOrder(t *testing.T) {
	first := errors.New("first failed")
	second := errors.New("second failed")
	secondDone := make(chan struct{})
	err := runBuildTasks(
		buildTask{name: "first stage", run: func() error {
			<-secondDone
			return first
		}},
		buildTask{name: "second stage", run: func() error {
			close(secondDone)
			return second
		}},
	)
	if !errors.Is(err, first) {
		t.Fatalf("expected the first declared error, got %v", err)
	}
	if !strings.Contains(err.Error(), "first stage") {
		t.Fatalf("expected the stage name in the error, got %v", err)
	}
}
