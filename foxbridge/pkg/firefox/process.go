package firefox

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chengwee/cdp-camoufox/foxbridge/pkg/backend"
	"github.com/chengwee/cdp-camoufox/foxbridge/pkg/backend/juggler"
)

// Config holds the Firefox launch configuration.
type Config struct {
	// Path to the Firefox/Camoufox binary. If empty, auto-detected.
	BinaryPath string
	// Extra Firefox arguments.
	ExtraArgs []string
	// Headless mode.
	Headless bool
	// Profile directory.
	ProfileDir string
	// BiDiPort enables WebDriver BiDi on this port. 0 = disabled.
	BiDiPort int
	// PageWorldEval disables isolation for the unnamed default world.
	PageWorldEval bool
}

// Process manages a Firefox/Camoufox browser process.
type Process struct {
	cmd       *exec.Cmd
	client    *juggler.Client
	transport *juggler.PipeTransport
	bidiPort  int
	startedAt time.Time
	waited    bool
	mu        sync.Mutex
}

// New creates a new Process instance without starting it.
func New() *Process {
	return &Process{}
}

// Start launches Firefox and establishes the Juggler pipe connection.
func (p *Process) Start(cfg Config) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cmd != nil {
		return fmt.Errorf("process already running")
	}
	if cfg.PageWorldEval && cfg.BiDiPort > 0 {
		return fmt.Errorf("page-world evaluation requires the Juggler backend, not BiDi")
	}

	bin := cfg.BinaryPath
	if bin == "" {
		var err error
		bin, err = findBinary()
		if err != nil {
			return err
		}
	}

	args := []string{
		"--no-remote",
	}

	// BiDi-only mode: don't use Juggler pipes (standard Firefox doesn't have Juggler)
	biDiOnly := cfg.BiDiPort > 0

	if !biDiOnly {
		args = append(args, "--juggler-pipe", "--purgecaches")
	}
	if cfg.Headless {
		args = append(args, "--headless")
	}
	if cfg.ProfileDir != "" {
		args = append(args, "--profile", cfg.ProfileDir)
	}
	if cfg.BiDiPort > 0 {
		args = append(args, fmt.Sprintf("--remote-debugging-port=%d", cfg.BiDiPort))
	}
	args = append(args, cfg.ExtraArgs...)

	cmd := exec.Command(bin, args...)
	if cfg.PageWorldEval {
		var err error
		cmd.Env, err = pageWorldEnvironment(os.Environ())
		if err != nil {
			return err
		}
	}
	// Keep browser diagnostics visible to the process supervisor/container logs.
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if !biDiOnly {
		// Create pipes for Juggler transport (FD 3 read, FD 4 write from Firefox's perspective).
		toFirefoxRead, toFirefoxWrite, err := os.Pipe()
		if err != nil {
			return fmt.Errorf("create pipe to firefox: %w", err)
		}
		fromFirefoxRead, fromFirefoxWrite, err := os.Pipe()
		if err != nil {
			toFirefoxRead.Close()
			toFirefoxWrite.Close()
			return fmt.Errorf("create pipe from firefox: %w", err)
		}

		// FD 0=stdin, 1=stdout, 2=stderr, 3=juggler-read (from us), 4=juggler-write (to us)
		cmd.ExtraFiles = []*os.File{toFirefoxRead, fromFirefoxWrite}

		if err := cmd.Start(); err != nil {
			toFirefoxRead.Close()
			toFirefoxWrite.Close()
			fromFirefoxRead.Close()
			fromFirefoxWrite.Close()
			return fmt.Errorf("start firefox: %w", err)
		}

		// Close the ends we don't use.
		toFirefoxRead.Close()
		fromFirefoxWrite.Close()

		transport := juggler.NewPipeTransport(fromFirefoxRead, toFirefoxWrite)
		client := juggler.NewClient(transport)

		p.client = client
		p.transport = transport
	} else {
		// BiDi-only: no pipes, just start the process
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start firefox: %w", err)
		}
	}

	p.cmd = cmd
	p.bidiPort = cfg.BiDiPort
	p.startedAt = time.Now()

	return nil
}

func pageWorldEnvironment(environment []string) ([]string, error) {
	values := make(map[string]string)
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	var configText strings.Builder
	for index := 1; ; index++ {
		chunk, ok := values["CAMOU_CONFIG_"+strconv.Itoa(index)]
		if !ok {
			break
		}
		configText.WriteString(chunk)
	}
	configJSON := configText.String()
	if configJSON == "" {
		configJSON = values["CAMOU_CONFIG"]
	}
	config := make(map[string]json.RawMessage)
	if configJSON != "" {
		if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
			return nil, fmt.Errorf("invalid CAMOU_CONFIG JSON: %w", err)
		}
		if config == nil {
			return nil, fmt.Errorf("invalid CAMOU_CONFIG JSON: expected an object")
		}
	}
	config["disableWorldIsolation"] = json.RawMessage("true")
	encoded, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("encode CAMOU_CONFIG: %w", err)
	}
	filtered := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if key == "CAMOU_CONFIG" {
			continue
		}
		if suffix, ok := strings.CutPrefix(key, "CAMOU_CONFIG_"); ok {
			if _, err := strconv.Atoi(suffix); err == nil {
				continue
			}
		}
		filtered = append(filtered, entry)
	}
	chunkSize := 32767
	if runtime.GOOS == "windows" {
		chunkSize = 2047
	}
	for index := 1; len(encoded) > 0; index++ {
		end := min(chunkSize, len(encoded))
		for end < len(encoded) && encoded[end]&0xc0 == 0x80 {
			end--
		}
		filtered = append(filtered, fmt.Sprintf("CAMOU_CONFIG_%d=%s", index, encoded[:end]))
		encoded = encoded[end:]
	}
	return filtered, nil
}

// Client returns the Juggler backend.
func (p *Process) Client() backend.Backend {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.client
}

// PID returns the Firefox process ID, or 0 if not running.
func (p *Process) PID() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd != nil && p.cmd.Process != nil {
		return p.cmd.Process.Pid
	}
	return 0
}

// BiDiURL returns the WebDriver BiDi WebSocket URL, or empty if BiDi is not enabled.
func (p *Process) BiDiURL() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bidiPort > 0 {
		return fmt.Sprintf("ws://127.0.0.1:%d/session", p.bidiPort)
	}
	return ""
}

// Running returns true if the process is alive.
func (p *Process) Running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cmd != nil && p.cmd.ProcessState == nil
}

// Stop gracefully shuts down the Firefox process.
func (p *Process) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.client != nil {
		// Juggler may terminate Firefox without replying to Browser.close. Do not
		// leave a container supervisor blocked for the normal 30-second RPC timeout.
		client := p.client
		closeDone := make(chan struct{})
		go func() {
			_, _ = client.Call("", "Browser.close", nil)
			close(closeDone)
		}()
		select {
		case <-closeDone:
		case <-time.After(2 * time.Second):
		}
		p.client.Close()
		p.client = nil
	}

	if p.cmd != nil && p.cmd.Process != nil && !p.waited {
		done := make(chan error, 1)
		go func() { done <- p.cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			p.cmd.Process.Kill()
			<-done
		}
		p.waited = true
		p.cmd = nil
	}

	return nil
}

// Wait blocks until the Firefox process exits.
func (p *Process) Wait() error {
	p.mu.Lock()
	cmd := p.cmd
	if cmd == nil || p.waited {
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()

	err := cmd.Wait()

	p.mu.Lock()
	p.waited = true
	p.mu.Unlock()

	return err
}

// findBinary locates a Firefox/Camoufox binary.
func findBinary() (string, error) {
	execPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("get executable path: %w", err)
	}
	dir := filepath.Dir(execPath)

	candidates := []string{"camoufox", "camoufox-bin", "firefox", "firefox-bin"}
	if runtime.GOOS == "darwin" {
		candidates = append(candidates, "../MacOS/camoufox", "../MacOS/firefox")
	}
	if runtime.GOOS == "windows" {
		candidates = []string{"camoufox.exe", "firefox.exe"}
	}

	for _, name := range candidates {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	// Try PATH
	for _, name := range []string{"camoufox", "camoufox-bin", "firefox"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}

	return "", fmt.Errorf("firefox/camoufox binary not found (looked in %s and PATH)", dir)
}
