package router

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"syscall"

	"go.uber.org/zap"
)

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
