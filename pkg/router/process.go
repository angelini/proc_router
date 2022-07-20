package router

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"

	"go.uber.org/zap"
)

type Process struct {
	log        *zap.Logger
	executable string
	script     string
	port       int
	version    int

	pid        *int
	cancelFunc context.CancelFunc
}

func NewProcess(log *zap.Logger, executable, script string, port, version int) *Process {
	return &Process{
		log:        log,
		executable: executable,
		script:     script,
		port:       port,
		version:    version,
	}
}

func (p *Process) Run(parentCtx context.Context) error {
	ctx, cancel := context.WithCancel(parentCtx)
	p.cancelFunc = cancel

	cmd := exec.CommandContext(ctx, p.executable, p.script)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PR_PORT=%d", p.port),
		fmt.Sprintf("PR_VERSION=%d", p.version),
	)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to pipe stdout: %w", err)
	}

	stderrPipe, err := cmd.StderrPipe()
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
	err = cmd.Start()
	if err != nil {
		return fmt.Errorf("cannot start process [%v %v]: %w", p.executable, p.script, err)
	}

	p.pid = &cmd.Process.Pid
	return nil
}

func (p *Process) Kill() error {
	if p.pid == nil {
		return errors.New("cannot kill process that wasn't started")
	}

	err := syscall.Kill(*p.pid, syscall.SIGINT)
	if err != nil {
		return err
	}

	p.cancelFunc()
	return nil
}
