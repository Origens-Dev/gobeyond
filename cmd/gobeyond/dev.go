package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Origens-Dev/gobeyond/internal/project"
)

const (
	defaultDevPort = 3000
	devPollPeriod  = 400 * time.Millisecond
)

type devOptions struct {
	port int
}

func parseDevOptions(args []string) (devOptions, error) {
	set := flag.NewFlagSet("dev", flag.ContinueOnError)
	port := set.Int("port", defaultDevPort, "public development server port")
	set.IntVar(port, "p", defaultDevPort, "public development server port")
	if err := set.Parse(args); err != nil {
		return devOptions{}, err
	}
	if set.NArg() != 0 {
		return devOptions{}, errors.New("usage: gobeyond dev [--port PORT]")
	}
	if *port < 1 || *port > 65535 {
		return devOptions{}, errors.New("development port must be between 1 and 65535")
	}
	return devOptions{port: *port}, nil
}

func dev(root string, args []string) error {
	options, err := parseDevOptions(args)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runDev(ctx, root, options)
}

func runDev(ctx context.Context, root string, options devOptions) error {
	environment, err := projectEnvironment(websiteRoot(root), "development")
	if err != nil {
		return err
	}
	publicAddress := net.JoinHostPort("127.0.0.1", strconv.Itoa(options.port))
	listener, err := net.Listen("tcp", publicAddress)
	if err != nil {
		return fmt.Errorf("listen on development port %d: %w", options.port, err)
	}

	generatedRoot := filepath.Join(root, ".gobeyond")
	if err := os.MkdirAll(generatedRoot, 0o755); err != nil {
		_ = listener.Close()
		return err
	}
	workspace, err := os.MkdirTemp(generatedRoot, "dev-")
	if err != nil {
		_ = listener.Close()
		return err
	}
	defer os.RemoveAll(workspace)

	gateway := newDevGateway()
	publicServer := &http.Server{
		Handler:           gateway,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- publicServer.Serve(listener)
	}()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = publicServer.Shutdown(shutdown)
	}()

	publicOrigin := "http://localhost:" + strconv.Itoa(options.port)
	fmt.Printf("GoBeyond dev building initial server for %s\n", publicOrigin)
	buildNumber := 1
	initialBuild := filepath.Join(workspace, fmt.Sprintf("build-%06d", buildNumber))
	if err := buildToModeWithCompilerAndEnvironment(root, initialBuild, false, "", environment, "development"); err != nil {
		return fmt.Errorf("initial development build: %w", err)
	}
	current, err := startDevBackend(ctx, root, initialBuild, publicOrigin, environment)
	if err != nil {
		return err
	}
	defer func() { current.stop() }()
	gateway.switchTarget(current.target)
	fmt.Printf("GoBeyond dev ready on %s (internal %s)\n", publicOrigin, current.target.Host)
	compilerCLI, err := preparedCompilerCLI(root)
	if err != nil {
		return err
	}

	builtSnapshot, err := project.BuildSnapshot(root)
	if err != nil {
		return err
	}
	observedSnapshot := builtSnapshot
	ticker := time.NewTicker(devPollPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-serveResult:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-ticker.C:
			nextSnapshot, snapshotErr := project.BuildSnapshot(root)
			if snapshotErr != nil {
				fmt.Fprintln(os.Stderr, "GoBeyond dev watch:", snapshotErr)
				continue
			}
			if devSnapshotsEqual(observedSnapshot, nextSnapshot) {
				continue
			}
			observedSnapshot = nextSnapshot
			rebuild := classifyDevRebuild(builtSnapshot, nextSnapshot, root, websiteRoot(root))
			if rebuild == devRebuildNone {
				continue
			}
			gateway.broadcast(devEvent{name: "building"})
			buildStarted := time.Now()
			buildNumber++
			candidateBuild := filepath.Join(workspace, fmt.Sprintf("build-%06d", buildNumber))
			var buildErr error
			if rebuild == devRebuildGoOnly {
				fmt.Println("GoBeyond dev Go-only change detected; rebuilding server")
				buildErr = buildDevGoServer(root, current.buildDirectory, candidateBuild, environment)
			} else {
				fmt.Println("GoBeyond dev frontend or structural change detected; building complete replacement")
				selectedCompiler := compilerCLI
				if devCompilerInputsChanged(builtSnapshot, nextSnapshot, root) {
					selectedCompiler = ""
				}
				buildErr = buildToModeWithCompilerAndEnvironment(root, candidateBuild, false, selectedCompiler, environment, "development")
				if buildErr == nil {
					compilerCLI, buildErr = preparedCompilerCLI(root)
				}
			}
			if buildErr != nil {
				_ = os.RemoveAll(candidateBuild)
				fmt.Fprintln(os.Stderr, "GoBeyond dev build failed; keeping current server:", buildErr)
				gateway.broadcast(devEvent{name: "build-error", data: buildErr.Error()})
				continue
			}
			candidate, err := startDevBackend(ctx, root, candidateBuild, publicOrigin, environment)
			if err != nil {
				_ = os.RemoveAll(candidateBuild)
				fmt.Fprintln(os.Stderr, "GoBeyond dev replacement failed; keeping current server:", err)
				gateway.broadcast(devEvent{name: "build-error", data: err.Error()})
				continue
			}

			previous := current
			current = candidate
			builtSnapshot = nextSnapshot
			gateway.switchTarget(candidate.target)
			gateway.broadcast(devEvent{name: "reload"})
			fmt.Printf("GoBeyond dev switched to internal %s in %s\n", candidate.target.Host, time.Since(buildStarted).Round(time.Millisecond))
			go func(backend *devBackend, buildDirectory string) {
				backend.stop()
				_ = os.RemoveAll(buildDirectory)
			}(previous, previous.buildDirectory)
		}
	}
}

type devRebuildMode uint8

const (
	devRebuildNone devRebuildMode = iota
	devRebuildGoOnly
	devRebuildFull
)

func classifyDevRebuild(previous, next map[string]string, root, website string) devRebuildMode {
	serverRoot, err := filepath.Rel(root, filepath.Join(website, "server"))
	if err != nil {
		return devRebuildFull
	}
	serverPrefix := strings.TrimSuffix(filepath.ToSlash(serverRoot), "/") + "/"
	appRoot, err := filepath.Rel(root, filepath.Join(website, "app"))
	if err != nil {
		return devRebuildFull
	}
	appPrefix := strings.TrimSuffix(filepath.ToSlash(appRoot), "/") + "/"
	internalRoot, err := filepath.Rel(root, filepath.Join(website, "internal"))
	if err != nil {
		return devRebuildFull
	}
	internalPrefix := strings.TrimSuffix(filepath.ToSlash(internalRoot), "/") + "/"
	changed := false
	for path, previousDigest := range previous {
		nextDigest, exists := next[path]
		if !exists {
			return devRebuildFull
		}
		if nextDigest == previousDigest {
			continue
		}
		changed = true
		if !devGoOnlyPath(path, serverPrefix, appPrefix, internalPrefix) {
			return devRebuildFull
		}
	}
	for path := range next {
		if _, exists := previous[path]; !exists {
			return devRebuildFull
		}
	}
	if changed {
		return devRebuildGoOnly
	}
	return devRebuildNone
}

func devGoOnlyPath(file, serverPrefix, appPrefix, internalPrefix string) bool {
	if (strings.HasPrefix(file, serverPrefix) || strings.HasPrefix(file, internalPrefix)) && filepath.Ext(file) == ".go" {
		return true
	}
	if !strings.HasPrefix(file, appPrefix) {
		return false
	}
	relative := strings.TrimPrefix(file, appPrefix)
	base := path.Base(relative)
	if base == "page.go" || base == "actions.go" {
		return true
	}
	return strings.HasPrefix(relative, "api/") && base == "route.go"
}

func devSnapshotsEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for path, digest := range left {
		if right[path] != digest {
			return false
		}
	}
	return true
}

func devCompilerInputsChanged(previous, next map[string]string, root string) bool {
	if _, err := os.Stat(filepath.Join(root, "packages", "compiler", "tsconfig.json")); err != nil {
		return false
	}
	const compilerPrefix = "packages/compiler/"
	for path, previousDigest := range previous {
		if strings.HasPrefix(path, compilerPrefix) && next[path] != previousDigest {
			return true
		}
	}
	for path := range next {
		if strings.HasPrefix(path, compilerPrefix) {
			if _, exists := previous[path]; !exists {
				return true
			}
		}
	}
	return false
}

func buildDevGoServer(root, currentBuild, candidateBuild string, environment []string) error {
	if err := copyTree(currentBuild, candidateBuild); err != nil {
		return fmt.Errorf("copy current development build: %w", err)
	}
	website := websiteRoot(root)
	routes, err := project.Discover(website)
	if err != nil {
		return err
	}
	if err := project.SyncGoSources(website, routes, false); err != nil {
		return fmt.Errorf("project route Go sources: %w", err)
	}
	target, err := serverBuildTarget(website)
	if err != nil {
		return err
	}
	serverOutput := filepath.Join(candidateBuild, "server", "gobeyond-server")
	if err := runCommandWithEnvironment(root, environment, "go", "build", "-trimpath", "-ldflags=-s -w", "-o", serverOutput, target); err != nil {
		return fmt.Errorf("build Go server: %w", err)
	}
	return nil
}

type devBackend struct {
	buildDirectory string
	command        *exec.Cmd
	done           chan error
	target         *url.URL
	stopOnce       sync.Once
}

func startDevBackend(ctx context.Context, root, buildDirectory, publicOrigin string, environment []string) (*devBackend, error) {
	manifestData, err := os.ReadFile(filepath.Join(buildDirectory, "server", "runtime-manifest.json"))
	if err != nil {
		return nil, err
	}
	var manifest struct {
		BuildID string `json:"buildId"`
	}
	if err := json.Unmarshal(manifestData, &manifest); err != nil || manifest.BuildID == "" {
		return nil, errors.New("development runtime manifest is invalid")
	}

	address, err := availableLoopbackAddress()
	if err != nil {
		return nil, err
	}
	target, _ := url.Parse("http://" + address)
	command := exec.CommandContext(ctx, filepath.Join(buildDirectory, "server", "gobeyond-server"))
	command.Dir = root
	command.Env = withEnvironment(environment,
		"GOBEYOND_ADDR="+address,
		"GOBEYOND_BUILD_ID="+manifest.BuildID,
		"GOBEYOND_PUBLIC_ORIGIN="+publicOrigin,
		"GOBEYOND_PLAN_DIR="+filepath.Join(buildDirectory, "server", "render-plans"),
		"GOBEYOND_RUNTIME_DATA_DIR="+filepath.Join(buildDirectory, "server", "runtime-data"),
		"GOBEYOND_STATIC_DIR="+filepath.Join(buildDirectory, "static"),
	)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err := command.Start(); err != nil {
		return nil, err
	}
	backend := &devBackend{
		buildDirectory: buildDirectory,
		command:        command,
		done:           make(chan error, 1),
		target:         target,
	}
	go func() { backend.done <- command.Wait() }()
	if err := waitForDevBackend(backend, publicOrigin); err != nil {
		backend.stop()
		return nil, err
	}
	return backend, nil
}

func availableLoopbackAddress() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return "", err
	}
	return address, nil
}

func waitForDevBackend(backend *devBackend, publicOrigin string) error {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-backend.done:
			backend.done <- err
			if err == nil {
				return errors.New("development server exited before becoming ready")
			}
			return fmt.Errorf("development server exited before becoming ready: %w", err)
		default:
		}
		request, _ := http.NewRequest(http.MethodGet, backend.target.String()+"/__gobeyond/readyz", nil)
		request.Host = strings.TrimPrefix(publicOrigin, "http://")
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("development server readiness timed out")
}

func (backend *devBackend) stop() {
	backend.stopOnce.Do(func() {
		if backend.command.Process == nil {
			return
		}
		_ = backend.command.Process.Signal(os.Interrupt)
		select {
		case <-backend.done:
			return
		case <-time.After(12 * time.Second):
			_ = backend.command.Process.Kill()
			<-backend.done
		}
	})
}

type devEvent struct {
	name string
	data string
}

type devGateway struct {
	targetMu sync.RWMutex
	target   *url.URL
	proxy    *httputil.ReverseProxy

	eventMu     sync.Mutex
	subscribers map[chan devEvent]struct{}
}

func newDevGateway() *devGateway {
	gateway := &devGateway{subscribers: make(map[chan devEvent]struct{})}
	gateway.proxy = &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			target := gateway.currentTarget()
			request.SetURL(target)
			request.Out.Host = request.In.Host
			request.Out.Header.Del("Accept-Encoding")
		},
		ModifyResponse: gateway.injectReloadClient,
		ErrorHandler: func(writer http.ResponseWriter, _ *http.Request, err error) {
			http.Error(writer, "GoBeyond development server is restarting: "+err.Error(), http.StatusBadGateway)
		},
	}
	return gateway
}

func (gateway *devGateway) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/_gobeyond/dev/events":
		gateway.serveEvents(writer, request)
		return
	case "/_gobeyond/dev/client.js":
		writer.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		writer.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(writer, devClientSource)
		return
	}
	if gateway.currentTarget() == nil {
		http.Error(writer, "GoBeyond development server is building", http.StatusServiceUnavailable)
		return
	}
	gateway.proxy.ServeHTTP(writer, request)
}

func (gateway *devGateway) currentTarget() *url.URL {
	gateway.targetMu.RLock()
	defer gateway.targetMu.RUnlock()
	if gateway.target == nil {
		return nil
	}
	copyTarget := *gateway.target
	return &copyTarget
}

func (gateway *devGateway) switchTarget(target *url.URL) {
	gateway.targetMu.Lock()
	defer gateway.targetMu.Unlock()
	copyTarget := *target
	gateway.target = &copyTarget
}

func (gateway *devGateway) injectReloadClient(response *http.Response) error {
	if !strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/html") {
		return nil
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	_ = response.Body.Close()
	script := []byte(`<script type="module" src="/_gobeyond/dev/client.js"></script>`)
	if closing := bytes.LastIndex(bytes.ToLower(body), []byte("</body>")); closing >= 0 {
		body = append(append(append([]byte(nil), body[:closing]...), script...), body[closing:]...)
	} else {
		body = append(body, script...)
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.ContentLength = int64(len(body))
	response.Header.Set("Content-Length", strconv.Itoa(len(body)))
	response.Header.Set("Cache-Control", "no-store")
	response.Header.Del("ETag")
	return nil
}

func (gateway *devGateway) serveEvents(writer http.ResponseWriter, request *http.Request) {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		http.Error(writer, "streaming is unavailable", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Connection", "keep-alive")
	subscriber := make(chan devEvent, 4)
	gateway.eventMu.Lock()
	gateway.subscribers[subscriber] = struct{}{}
	gateway.eventMu.Unlock()
	defer func() {
		gateway.eventMu.Lock()
		delete(gateway.subscribers, subscriber)
		gateway.eventMu.Unlock()
	}()
	_, _ = io.WriteString(writer, "event: ready\ndata: true\n\n")
	flusher.Flush()
	for {
		select {
		case <-request.Context().Done():
			return
		case event := <-subscriber:
			encoded, _ := json.Marshal(event.data)
			fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", event.name, encoded)
			flusher.Flush()
		}
	}
}

func (gateway *devGateway) broadcast(event devEvent) {
	gateway.eventMu.Lock()
	defer gateway.eventMu.Unlock()
	for subscriber := range gateway.subscribers {
		select {
		case subscriber <- event:
		default:
		}
	}
}

const devClientSource = `
const source = new EventSource('/_gobeyond/dev/events')
const overlayId = '__gobeyond_dev_error__'
function removeOverlay() { document.getElementById(overlayId)?.remove() }
source.addEventListener('reload', () => location.reload())
source.addEventListener('build-error', (event) => {
  removeOverlay()
  const overlay = document.createElement('pre')
  overlay.id = overlayId
  overlay.setAttribute('role', 'alert')
  Object.assign(overlay.style, {
    position: 'fixed', inset: '1rem', zIndex: '2147483647', overflow: 'auto',
    margin: '0', padding: '1rem', color: '#fee', background: '#260b0b',
    border: '1px solid #f66', borderRadius: '0.5rem', whiteSpace: 'pre-wrap'
  })
  try { overlay.textContent = 'GoBeyond build failed\n\n' + JSON.parse(event.data) }
  catch { overlay.textContent = 'GoBeyond build failed' }
  document.body.append(overlay)
})
source.addEventListener('building', removeOverlay)
`
