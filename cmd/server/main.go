package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	std_log "log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"go.uber.org/zap"
)

const (
	portStart = 30000
)

var portOffset = 0

type cliArgs struct {
	port   int
	script string
}

func parseArgs() *cliArgs {
	port := flag.Int("port", 5251, "Server's port (default: 5251)")
	script := flag.String("script", "", "Script to execute")

	flag.Parse()

	return &cliArgs{
		port:   *port,
		script: *script,
	}
}

type VM struct {
	executable string
}

type Proc struct {
	port int
	cmd  *exec.Cmd
}

func NewProc(ctx context.Context, vm *VM, script string) *Proc {
	portOffset += 1
	port := portStart + portOffset
	cmd := exec.CommandContext(ctx, vm.executable, script)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PR_PORT=%d", port),
	)

	return &Proc{
		port: port,
		cmd:  cmd,
	}
}

func (p *Proc) Run(log *zap.Logger) error {
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
					log.Info("stdout EOF", zap.Int("port", p.port))
					break
				}
				if err != nil {
					log.Error("failed to read stdout line", zap.Error(err))
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
					log.Info("stderr EOF", zap.Int("port", p.port))
					break
				}
				if err != nil {
					log.Error("failed to read stderr line", zap.Error(err))
					return
				}
				stdout <- string(line)
			}
		}()

		for {
			select {
			case msg := <-stdout:
				log.Info("stdout", zap.String("msg", msg), zap.Int("port", p.port))
			case msg := <-stderr:
				log.Info("stderr", zap.String("msg", msg), zap.Int("port", p.port))
			}
		}
	}()

	log.Info("start proc", zap.Int("port", p.port))
	return p.cmd.Start()
}

func (p *Proc) Kill() error {
	if p.cmd.Process == nil {
		return errors.New("cannot kill process that wasn't started")
	}

	return syscall.Kill(p.cmd.Process.Pid, syscall.SIGINT)
}

func copyHeader(dest, src http.Header) {
	for key, value := range src {
		for _, nested := range value {
			dest.Add(key, nested)
		}
	}
}

func main() {
	ctx := context.Background()
	log, err := zap.NewDevelopment()
	if err != nil {
		std_log.Fatalf("Error building zap %v", err)
	}

	args := parseArgs()
	vm := &VM{executable: "node"}
	proc := NewProc(ctx, vm, args.script)

	err = proc.Run(log)
	if err != nil {
		log.Fatal("cannot start proc", zap.Error(err))
	}

	client := &http.Client{}

	http.HandleFunc("/", func(resp http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			log.Error("failed to read request body", zap.Error(err))
			http.Error(resp, err.Error(), http.StatusInternalServerError)
			return
		}

		url := fmt.Sprintf("http://127.0.0.1:%d/", proc.port)

		proxyReq, err := http.NewRequest(req.Method, url, bytes.NewReader(body))
		if err != nil {
			log.Error("failed to create proxy request", zap.Error(err))
			http.Error(resp, err.Error(), http.StatusInternalServerError)
			return
		}

		proxyReq.Header = make(http.Header)
		copyHeader(proxyReq.Header, req.Header)

		proxyResp, err := client.Do(proxyReq)
		if err != nil {
			log.Error("failed to proxy request", zap.Error(err))
			http.Error(resp, err.Error(), http.StatusInternalServerError)
			return
		}
		defer proxyResp.Body.Close()

		copyHeader(resp.Header(), proxyResp.Header)
		io.Copy(resp, proxyResp.Body)
	})

	log.Info("start routing server", zap.Int("port", args.port), zap.String("script", args.script))
	http.ListenAndServe("127.0.0.1:"+strconv.Itoa(args.port), nil)
}
