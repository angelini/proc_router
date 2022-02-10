package router

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"go.uber.org/zap"
)

const (
	NEXT_PROCESS_HEALTHY_INTERVAL = 500 * time.Millisecond
	OLD_PROCESS_GRACEFUL_INTERVAL = 2 * time.Second
)

type Manager struct {
	Host string

	log        *zap.Logger
	executable string
	script     string
	portStart  int
	portOffset int
	cancelFunc context.CancelFunc

	procMutex sync.RWMutex
	counters  map[int]int
	current   *Process
	next      *Process
	gracefuls []*Process
}

func NewManager(parentCtx context.Context, log *zap.Logger, host, executable, script string, portStart int) *Manager {
	ctx, cancel := context.WithCancel(parentCtx)

	manager := &Manager{
		Host:       host,
		log:        log,
		executable: executable,
		script:     script,
		portStart:  portStart,
		cancelFunc: cancel,
		counters:   make(map[int]int),
	}

	go func() {
		client := &http.Client{}

		for {
			select {
			case <-ctx.Done():
				return
			default:
				nextPort := manager.getNextPort()
				if nextPort == -1 {
					time.Sleep(NEXT_PROCESS_HEALTHY_INTERVAL)
					continue
				}

				url := fmt.Sprintf("http://%s:%d/health", host, nextPort)
				resp, err := client.Get(url)
				if err != nil {
					log.Info("could not connect", zap.String("url", url))
					time.Sleep(NEXT_PROCESS_HEALTHY_INTERVAL)
					continue
				}

				if resp.StatusCode == http.StatusOK {
					log.Info("successful connection, upgrading to current", zap.String("url", url))
					manager.setCurrent(nextPort)
				}
			}
		}
	}()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				for _, oldProc := range manager.gracefuls {
					if manager.remainingRequests(oldProc.port) == 0 {
						oldProc.Kill()
						manager.removeGraceful(oldProc.port)
					}
				}
				time.Sleep(OLD_PROCESS_GRACEFUL_INTERVAL)
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

	for _, oldProc := range m.gracefuls {
		oldProc.Kill()
	}

	m.cancelFunc()
}

func (m *Manager) remainingRequests(port int) int {
	m.procMutex.RLock()
	defer m.procMutex.RUnlock()

	return m.counters[port]
}

func (m *Manager) removeGraceful(port int) {
	m.procMutex.Lock()
	defer m.procMutex.Unlock()

	for index, oldProc := range m.gracefuls {
		if oldProc.port == port {
			m.gracefuls = append(m.gracefuls[:index], m.gracefuls[index+1:]...)
			return
		}
	}
}

func (m *Manager) getNextPort() int {
	m.procMutex.RLock()
	defer m.procMutex.RUnlock()

	if m.next == nil {
		return -1
	}

	return m.next.port
}

func (m *Manager) killNextIfRunning() error {
	m.procMutex.Lock()
	defer m.procMutex.Unlock()

	if m.next != nil {
		err := m.next.Kill()
		if err != nil {
			return err
		}
		m.next = nil
	}

	return nil
}

func (m *Manager) setNext(proc *Process) {
	m.procMutex.Lock()
	defer m.procMutex.Unlock()

	m.next = proc
}

func (m *Manager) setCurrent(port int) {
	m.procMutex.Lock()
	defer m.procMutex.Unlock()

	// Test if we're attempting to set an older 'next' as 'current'.
	if m.next.port != port {
		return
	}

	if m.current != nil {
		m.gracefuls = append(m.gracefuls, m.current)
	}

	m.current = m.next
	m.next = nil
}

func (m *Manager) StartProcess(ctx context.Context, version int) error {
	err := m.killNextIfRunning()
	if err != nil {
		return fmt.Errorf("failed to kill concurrent next process: %w", err)
	}

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

	err = proc.Run(ctx)
	if err != nil {
		return err
	}

	m.setNext(proc)
	return nil
}

func (m *Manager) IncrementRequestCounter(port int) {
	m.procMutex.Lock()
	defer m.procMutex.Unlock()

	m.counters[port] += 1
}

func (m *Manager) DecrementRequestCounter(port int) {
	m.procMutex.Lock()
	defer m.procMutex.Unlock()

	m.counters[port] -= 1

	if m.counters[port] == 0 {
		delete(m.counters, port)
	}
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
