package recorder

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// ProcessResult describes the final termination result of a process.
type ProcessResult struct {
	ExitCode int
	Signal   syscall.Signal
	Exited   bool
	Error    error
}

// ProcessHandle provides control over an executing child process.
type ProcessHandle interface {
	PID() int
	Signal(sig syscall.Signal) error
	Kill() error
	Wait() ProcessResult
	Done() <-chan struct{}
	HasExited() bool
	Result() (ProcessResult, bool)
}

// ProcessPipeline manages the coupled producer-consumer processes.
type ProcessPipeline interface {
	Producer() ProcessHandle
	Consumer() ProcessHandle
	ProgressReader() io.Reader
	StopOrderly(producerTimeout, finalizeTimeout, terminateTimeout, killTimeout time.Duration) error
	Wait() (producerRes ProcessResult, consumerRes ProcessResult)
	Close() error
}

// ProcessRunner abstracts process creation to enable testing without actual external tools.
type ProcessRunner interface {
	StartPipeline(
		producerArgs []string,
		consumerArgs []string,
	) (ProcessPipeline, error)
}

// realProcessHandle implements ProcessHandle using os/exec and Linux process groups.
type realProcessHandle struct {
	cmd       *exec.Cmd
	done      chan struct{}
	once      sync.Once
	mu        sync.RWMutex
	result    ProcessResult
	hasExited bool
}

func newRealProcessHandle(cmd *exec.Cmd) *realProcessHandle {
	return &realProcessHandle{
		cmd:  cmd,
		done: make(chan struct{}),
	}
}

func (h *realProcessHandle) PID() int {
	if h.cmd != nil && h.cmd.Process != nil {
		return h.cmd.Process.Pid
	}
	return 0
}

func (h *realProcessHandle) Signal(sig syscall.Signal) error {
	h.mu.RLock()
	if h.hasExited {
		h.mu.RUnlock()
		return nil
	}
	pid := h.PID()
	h.mu.RUnlock()

	if pid <= 0 {
		return errors.New("proceso no iniciado o PID inválido")
	}

	// In Linux, we set Setpgid: true on start.
	// Sending signal to -pid sends it to the entire process group.
	pgidErr := syscall.Kill(-pid, sig)
	if pgidErr == nil || errors.Is(pgidErr, syscall.ESRCH) {
		return nil
	}

	// Fallback to sending to single process
	if h.cmd.Process != nil {
		err := h.cmd.Process.Signal(sig)
		if err != nil && !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
			return err
		}
	}
	return nil
}

func (h *realProcessHandle) Kill() error {
	return h.Signal(syscall.SIGKILL)
}

func (h *realProcessHandle) Done() <-chan struct{} {
	return h.done
}

func (h *realProcessHandle) HasExited() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.hasExited
}

func (h *realProcessHandle) Result() (ProcessResult, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.result, h.hasExited
}

func (h *realProcessHandle) Wait() ProcessResult {
	<-h.done
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.result
}

func (h *realProcessHandle) monitorExit(err error) {
	h.once.Do(func() {
		h.mu.Lock()
		defer h.mu.Unlock()

		h.hasExited = true
		h.result = ProcessResult{
			Exited: true,
			Error:  err,
		}

		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				h.result.ExitCode = exitErr.ExitCode()
				if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
					h.result.Signal = status.Signal()
				}
			} else {
				h.result.ExitCode = -1
			}
		} else {
			h.result.ExitCode = 0
		}

		close(h.done)
	})
}

// realPipeline implements ProcessPipeline.
type realPipeline struct {
	producer       *realProcessHandle
	consumer       *realProcessHandle
	progressReader io.ReadCloser
	pipesToClose   []io.Closer
	mu             sync.Mutex
	stopOnce       sync.Once
	stopErr        error
}

func (p *realPipeline) Producer() ProcessHandle {
	return p.producer
}

func (p *realPipeline) Consumer() ProcessHandle {
	return p.consumer
}

func (p *realPipeline) ProgressReader() io.Reader {
	return p.progressReader
}

func (p *realPipeline) Wait() (ProcessResult, ProcessResult) {
	var prodRes, consRes ProcessResult
	if p.producer != nil {
		prodRes = p.producer.Wait()
	}
	if p.consumer != nil {
		consRes = p.consumer.Wait()
	}
	return prodRes, consRes
}

func (p *realPipeline) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var firstErr error
	for _, c := range p.pipesToClose {
		if err := c.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	p.pipesToClose = nil

	if p.progressReader != nil {
		if err := p.progressReader.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		p.progressReader = nil
	}
	return firstErr
}

func waitForProcess(h ProcessHandle, timeout time.Duration) bool {
	select {
	case <-h.Done():
		return true
	case <-time.After(timeout):
		return false
	}
}

func (p *realPipeline) StopOrderly(
	producerTimeout time.Duration,
	finalizeTimeout time.Duration,
	terminateTimeout time.Duration,
	killTimeout time.Duration,
) error {
	p.stopOnce.Do(func() {
		var errs []error

		// 1. Stop producer (Streamlink) gracefully with SIGTERM
		if p.producer != nil && !p.producer.HasExited() {
			_ = p.producer.Signal(syscall.SIGTERM)
			if !waitForProcess(p.producer, producerTimeout) {
				// Escalate to SIGKILL
				_ = p.producer.Kill()
				if !waitForProcess(p.producer, killTimeout) {
					errs = append(errs, fmt.Errorf("productor (PID %d) no confirmó su salida tras SIGKILL", p.producer.PID()))
				}
			}
		}

		// 2. Allow consumer (FFmpeg) to reach EOF and finalize cleanly
		if p.consumer != nil && !p.consumer.HasExited() {
			if !waitForProcess(p.consumer, finalizeTimeout) {
				// FFmpeg did not finish within finalizeTimeout, send SIGTERM
				slog.Error("Finalización agotó plazo, señalando consumidor con SIGTERM", "pid", p.consumer.PID())
				_ = p.consumer.Signal(syscall.SIGTERM)
				if !waitForProcess(p.consumer, terminateTimeout) {
					// Escalate to SIGKILL
					slog.Error("Forzando cierre de consumidor; el archivo podría quedar incompleto", "pid", p.consumer.PID())
					_ = p.consumer.Kill()
					if !waitForProcess(p.consumer, killTimeout) {
						errs = append(errs, fmt.Errorf("consumidor (PID %d) no confirmó su salida tras SIGKILL", p.consumer.PID()))
					}
				}
			}
		}

		// Close readers and descriptors
		_ = p.Close()

		if len(errs) > 0 {
			p.stopErr = errors.Join(errs...)
		}
	})

	return p.stopErr
}

// RealProcessRunner launches actual external processes on Linux.
type RealProcessRunner struct{}

func NewRealProcessRunner() *RealProcessRunner {
	return &RealProcessRunner{}
}

func (r *RealProcessRunner) StartPipeline( producerArgs []string, consumerArgs []string) (ProcessPipeline, error) {

	if len(producerArgs) == 0 || len(consumerArgs) == 0 {
		return nil, errors.New("argumentos de productor y consumidor son requeridos")
	}

	// Create an OS pipe for producer.stdout -> consumer.stdin
	pipeR, pipeW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("error al crear pipe de procesos: %w", err)
	}

	// Create an OS pipe for consumer.stdout -> progressReader
	progressR, progressW, err := os.Pipe()
	if err != nil {
		_ = pipeR.Close()
		_ = pipeW.Close()
		return nil, fmt.Errorf("error al crear pipe de progreso: %w", err)
	}

	producerCmd := exec.Command(producerArgs[0], producerArgs[1:]...)
	producerCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	producerCmd.Stdin = nil
	producerCmd.Stdout = pipeW
	producerCmd.Stderr = os.Stderr

	consumerCmd := exec.Command(consumerArgs[0], consumerArgs[1:]...)
	consumerCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	consumerCmd.Stdin = pipeR
	consumerCmd.Stdout = progressW
	consumerCmd.Stderr = os.Stderr

	// Start producer
	if err := producerCmd.Start(); err != nil {
		_ = pipeR.Close()
		_ = pipeW.Close()
		_ = progressR.Close()
		_ = progressW.Close()
		return nil, fmt.Errorf("error al iniciar productor (%s): %w", producerArgs[0], err)
	}
	producerHandle := newRealProcessHandle(producerCmd)

	// Start consumer
	if err := consumerCmd.Start(); err != nil {
		// Kill producer immediately if consumer fails to start
		_ = producerHandle.Kill()
		_ = pipeR.Close()
		_ = pipeW.Close()
		_ = progressR.Close()
		_ = progressW.Close()
		return nil, fmt.Errorf("error al iniciar consumidor (%s): %w", consumerArgs[0], err)
	}
	consumerHandle := newRealProcessHandle(consumerCmd)

	// Parent process must close its own copies of pipeW and pipeR so that
	// only producer has write-end and only consumer has read-end.
	// When producer terminates, write-end is fully closed and consumer receives EOF.
	_ = pipeW.Close()
	_ = pipeR.Close()
	// Also close progressW in parent so only consumer has the write-end
	_ = progressW.Close()

	// Launch background exit monitors
	go func() {
		err := producerCmd.Wait()
		producerHandle.monitorExit(err)
	}()

	go func() {
		err := consumerCmd.Wait()
		consumerHandle.monitorExit(err)
	}()

	pipeline := &realPipeline{
		producer:       producerHandle,
		consumer:       consumerHandle,
		progressReader: progressR,
	}

	return pipeline, nil
}
