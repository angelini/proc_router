package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	std_log "log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	"go.uber.org/zap"
)

type cliArgs struct {
	port   int
	host   string
	script string
}

func parseArgs() *cliArgs {
	port := flag.Int("port", 5251, "Server's port (default: 5251)")
	host := flag.String("host", "127.0.0.1", "Script server hostname")
	script := flag.String("script", "", "Script to execute")

	flag.Parse()

	return &cliArgs{
		port:   *port,
		host:   *host,
		script: *script,
	}
}

type Process struct {
	log        *zap.Logger
	port       int
	version    int
	cmd        *exec.Cmd
	cancelFunc context.CancelFunc
}

func (p *Process) Run(parentCtx context.Context) error {
	ctx, cancel := context.WithCancel(parentCtx)
	p.cancelFunc = cancel

	stdoutPipe, err := p.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to pipe stdout: %w", err)
	}

	stderrPipe, err := p.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to pipe stderr: %w", err)
	}

	go func() {
		stdout := make(chan string)
		go func() {
			reader := bufio.NewReader(stdoutPipe)
			for {
				line, _, err := reader.ReadLine()
				if err == io.EOF {
					p.log.Info("stdout EOF", zap.Int("port", p.port))
					break
				}
				if err != nil {
					p.log.Error("failed to read stdout line", zap.Error(err))
					return
				}
				stdout <- string(line)
			}
		}()

		stderr := make(chan string)
		go func() {
			reader := bufio.NewReader(stderrPipe)
			for {
				line, _, err := reader.ReadLine()
				if err == io.EOF {
					p.log.Info("stderr EOF", zap.Int("port", p.port))
					break
				}
				if err != nil {
					p.log.Error("failed to read stderr line", zap.Error(err))
					return
				}
				stdout <- string(line)
			}
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-stdout:
				p.log.Info("stdout", zap.String("msg", msg), zap.Int("port", p.port))
			case msg := <-stderr:
				p.log.Info("stderr", zap.String("msg", msg), zap.Int("port", p.port))
			}
		}
	}()

	p.log.Info("start proc", zap.Int("port", p.port))
	return p.cmd.Start()
}

func (p *Process) Kill() error {
	if p.cmd.Process == nil {
		return errors.New("cannot kill process that wasn't started")
	}

	err := syscall.Kill(p.cmd.Process.Pid, syscall.SIGINT)
	if err != nil {
		return err
	}

	p.cancelFunc()
	return nil
}

type Manager struct {
	log        *zap.Logger
	host       string
	executable string
	script     string
	portStart  int
	portOffset int
	cancelFunc context.CancelFunc

	procMutex sync.RWMutex
	current   *Process
	next      *Process
}

func NewManager(parentCtx context.Context, log *zap.Logger, host, executable, script string, portStart int) *Manager {
	ctx, cancel := context.WithCancel(parentCtx)

	manager := &Manager{
		log:        log,
		host:       host,
		executable: executable,
		script:     script,
		portStart:  portStart,
		cancelFunc: cancel,
	}

	go func() {
		client := &http.Client{}

		for {
			select {
			case <-ctx.Done():
				return
			default:
				next := manager.GetNext()
				if next == nil {
					time.Sleep(500 * time.Millisecond)
					continue
				}

				url := fmt.Sprintf("http://%s:%d/", host, next.port)
				resp, err := client.Get(url)
				if err != nil {
					log.Info("could not connect", zap.String("url", url))
					time.Sleep(500 * time.Millisecond)
					continue
				}

				if resp.StatusCode == http.StatusOK {
					log.Info("successful connection, upgrading to current", zap.String("url", url))
					manager.SetCurrent(next)
				}
			}
		}
	}()

	return manager
}

func (m *Manager) Close() {
	m.procMutex.Lock()
	defer m.procMutex.Unlock()

	if m.next != nil {
		m.next.Kill()
	}

	if m.current != nil {
		m.current.Kill()
	}

	m.cancelFunc()
}

func (m *Manager) GetNext() *Process {
	m.procMutex.RLock()
	defer m.procMutex.RUnlock()

	return m.next
}

func (m *Manager) KillNextIfRunning() {
	m.procMutex.Lock()
	defer m.procMutex.Unlock()

	if m.next != nil {
		m.next.Kill()
		m.next = nil
	}
}

func (m *Manager) SetNext(proc *Process) {
	m.procMutex.Lock()
	defer m.procMutex.Unlock()

	m.next = proc
}

func (m *Manager) SetCurrent(proc *Process) {
	m.procMutex.Lock()
	defer m.procMutex.Unlock()

	if m.next == proc {
		if m.current != nil {
			m.current.Kill()
		}

		m.current = m.next
		m.next = nil
	}
}

func (m *Manager) StartProcess(ctx context.Context, version int) error {
	m.KillNextIfRunning()

	m.portOffset += 1
	port := m.portStart + m.portOffset
	cmd := exec.CommandContext(ctx, m.executable, m.script)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PR_PORT=%d", port),
		fmt.Sprintf("PR_VERSION=%d", version),
	)

	proc := &Process{
		log:     m.log,
		port:    port,
		version: version,
		cmd:     cmd,
	}

	err := proc.Run(ctx)
	if err != nil {
		return err
	}

	m.SetNext(proc)
	return nil
}

func (m *Manager) sendLivePort(portChan chan int) bool {
	m.procMutex.RLock()
	defer m.procMutex.RUnlock()

	if m.next == nil && m.current != nil {
		portChan <- m.current.port
		return true
	}
	return false
}

func (m *Manager) LivePortChannel(ctx context.Context) chan int {
	portChan := make(chan int, 1)
	foundPort := m.sendLivePort(portChan)

	if !foundPort {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				default:
					if m.sendLivePort(portChan) {
						return
					} else {
						time.Sleep(100 * time.Millisecond)
					}
				}
			}
		}()
	}

	return portChan
}

func copyHeader(dest, src http.Header) {
	for key, value := range src {
		for _, nested := range value {
			dest.Add(key, nested)
		}
	}
}

type VersionRequest struct {
	Version int
}

func httpErr(log *zap.Logger, resp http.ResponseWriter, err error, message string) {
	log.Error(message, zap.Error(err))
	http.Error(resp, err.Error(), http.StatusInternalServerError)
}

func main() {
	ctx := context.Background()
	log, err := zap.NewDevelopment()
	if err != nil {
		std_log.Fatalf("Error building zap %v", err)
	}

	args := parseArgs()
	client := &http.Client{}

	manager := NewManager(ctx, log, args.host, "node", args.script, 30000)
	defer manager.Close()

	http.HandleFunc("/__meta/version", func(resp http.ResponseWriter, req *http.Request) {
		var versionReq VersionRequest

		err := json.NewDecoder(req.Body).Decode(&versionReq)
		if err != nil {
			httpErr(log, resp, err, "failed to read proxy request body")
			return
		}

		err = manager.StartProcess(ctx, versionReq.Version)
		if err != nil {
			httpErr(log, resp, err, "failed to start process")
			return
		}

		fmt.Fprintf(resp, "Version: %d", versionReq.Version)
	})

	http.HandleFunc("/", func(resp http.ResponseWriter, req *http.Request) {
		reqCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		portChan := manager.LivePortChannel(reqCtx)

		select {
		case <-time.After(5 * time.Second):
			http.Error(resp, "timeout waiting for live port", http.StatusGatewayTimeout)
			log.Info("timeout waiting for live port", zap.String("url", req.URL.Path))

		case port := <-portChan:
			body, err := io.ReadAll(req.Body)
			if err != nil {
				httpErr(log, resp, err, "failed to read proxy request body")
				return
			}

			url := fmt.Sprintf("http://%s:%d/", manager.host, port)

			proxyReq, err := http.NewRequest(req.Method, url, bytes.NewReader(body))
			if err != nil {
				httpErr(log, resp, err, "failed to create proxy request")
				return
			}

			proxyReq.Header = make(http.Header)
			copyHeader(proxyReq.Header, req.Header)

			proxyResp, err := client.Do(proxyReq)
			if err != nil {
				httpErr(log, resp, err, "failed to proxy request")
				return
			}
			defer proxyResp.Body.Close()

			copyHeader(resp.Header(), proxyResp.Header)
			io.Copy(resp, proxyResp.Body)
		}
	})

	log.Info("start routing server", zap.Int("port", args.port), zap.String("script", args.script))
	http.ListenAndServe("127.0.0.1:"+strconv.Itoa(args.port), nil)
}
