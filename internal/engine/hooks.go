package engine

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Paintersrp/orco/internal/stack"
)

type hookExecutor interface {
	Run(ctx context.Context, svc *stack.Service, phase string, hook *stack.LifecycleHook) hookResult
}

type hookResult struct {
	Phase    string
	Command  []string
	Logs     []hookLog
	Err      error
	TimedOut bool
}

type hookLog struct {
	Message string
	Stream  string
}

const (
	hookStreamStdout = "stdout"
	hookStreamStderr = "stderr"
)

type commandHookExecutor struct {
	now func() time.Time
}

func newCommandHookExecutor() *commandHookExecutor {
	return &commandHookExecutor{now: time.Now}
}

func (e *commandHookExecutor) Run(ctx context.Context, svc *stack.Service, phase string, hook *stack.LifecycleHook) hookResult {
	res := hookResult{Phase: phase}
	if hook == nil || len(hook.Command) == 0 {
		return res
	}
	res.Command = append(res.Command, hook.Command...)
	if ctx == nil {
		ctx = context.Background()
	}
	if hook.Timeout.Duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, hook.Timeout.Duration)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, hook.Command[0], hook.Command[1:]...)
	cmd.Env = mergeServiceEnv(os.Environ(), svc)
	if svc != nil && svc.ResolvedWorkdir != "" {
		cmd.Dir = svc.ResolvedWorkdir
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		res.Err = err
		return res
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		res.Err = err
		return res
	}

	if err := cmd.Start(); err != nil {
		res.Err = err
		return res
	}

	logsCh := make(chan hookLog, 16)
	var wg sync.WaitGroup

	scan := func(r io.Reader, stream string) {
		defer wg.Done()
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			logsCh <- hookLog{Message: scanner.Text(), Stream: stream}
		}
	}

	wg.Add(2)
	go scan(stdout, hookStreamStdout)
	go scan(stderr, hookStreamStderr)

	go func() {
		wg.Wait()
		close(logsCh)
	}()

	waitErr := cmd.Wait()

	for entry := range logsCh {
		res.Logs = append(res.Logs, entry)
	}

	if waitErr != nil {
		if errors.Is(waitErr, context.DeadlineExceeded) {
			res.TimedOut = true
			res.Err = context.DeadlineExceeded
			return res
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			res.TimedOut = true
			res.Err = context.DeadlineExceeded
			return res
		}
		if errors.Is(ctx.Err(), context.Canceled) && !errors.Is(waitErr, context.Canceled) {
			res.Err = context.Canceled
			return res
		}
		res.Err = waitErr
		return res
	}

	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			res.TimedOut = true
		}
		res.Err = err
	}

	return res
}

func mergeServiceEnv(base []string, svc *stack.Service) []string {
	if svc == nil || len(svc.Env) == 0 {
		dup := append([]string(nil), base...)
		return dup
	}
	env := append([]string(nil), base...)
	keys := make([]string, 0, len(svc.Env))
	for k := range svc.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, fmt.Sprintf("%s=%s", k, svc.Env[k]))
	}
	return env
}

func joinCommand(cmd []string) string {
	if len(cmd) == 0 {
		return ""
	}
	parts := make([]string, len(cmd))
	copy(parts, cmd)
	for i, part := range parts {
		if strings.ContainsAny(part, " \t\n\"") {
			parts[i] = fmt.Sprintf("%q", part)
		}
	}
	return strings.Join(parts, " ")
}
