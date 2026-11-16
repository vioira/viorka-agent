package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/vioira/viorka-agent/internal/connection"
)

type enrollmentPrompt interface {
	Retry(replaceToken bool) bool
	Token() (string, error)
}

type terminalPrompt struct {
	ctx    context.Context
	tty    *os.File
	reader *bufio.Reader
}

// A controlling terminal also works when --token-stdin consumes a pipe.
// Nonblocking mode allows cancellation to interrupt a pending terminal read.
func openTerminal(ctx context.Context) (*terminalPrompt, func()) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, func() {}
	}
	stop := context.AfterFunc(ctx, func() { f.Close() })
	p := &terminalPrompt{ctx: ctx, tty: f}
	p.reader = bufio.NewReader(p)
	return p, func() { stop(); f.Close() }
}

// Some terminal devices cannot be registered with Go's I/O poller. Keep those
// nonblocking reads cancellable too, without a blocked reader goroutine.
func (p *terminalPrompt) Read(b []byte) (int, error) {
	for {
		if err := p.ctx.Err(); err != nil {
			return 0, err
		}
		n, err := p.tty.Read(b)
		if !errors.Is(err, syscall.EAGAIN) {
			return n, err
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-p.ctx.Done():
			timer.Stop()
			return 0, p.ctx.Err()
		case <-timer.C:
		}
	}
}

func (p *terminalPrompt) Retry(replaceToken bool) bool {
	message := "选择：1) 重试  2) 终止 [默认 2]: "
	if replaceToken {
		message = "选择：1) 重新输入注册令牌  2) 终止 [默认 2]: "
	}
	fmt.Fprint(p.tty, message)
	line, err := p.reader.ReadSlice('\n')
	return err == nil && p.ctx.Err() == nil && strings.TrimSpace(string(line)) == "1"
}

// stty is provided by coreutils on Debian/Alpine and by the OS on macOS.
// Open a separate descriptor so echo can be restored after a canceled read.
func terminalMode(args ...string) (string, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/stty", args...)
	cmd.Stdin = f
	cmd.Env = []string{"LC_ALL=C", "PATH=/usr/bin:/bin"}
	b, err := cmd.Output()
	return strings.TrimSpace(string(b)), err
}

func (p *terminalPrompt) Token() (token string, err error) {
	mode, err := terminalMode("-g")
	if err != nil {
		return "", fmt.Errorf("无法读取终端设置：%w", err)
	}
	if _, err = terminalMode("-echo"); err != nil {
		return "", fmt.Errorf("无法关闭令牌输入回显：%w", err)
	}
	defer func() {
		_, restoreErr := terminalMode(mode)
		if restoreErr != nil {
			err = errors.Join(err, fmt.Errorf("恢复终端回显失败：%w", restoreErr))
		}
		fmt.Fprintln(p.tty)
	}()
	fmt.Fprint(p.tty, "请输入 Panel 注册令牌: ")
	line, err := p.reader.ReadSlice('\n')
	if p.ctx.Err() != nil {
		return "", p.ctx.Err()
	}
	if err != nil {
		return "", fmt.Errorf("读取注册令牌失败：%w", err)
	}
	return strings.TrimSpace(string(line)), nil
}

func enrollWithRetry(ctx context.Context, token string, attempt func(context.Context, string) error, prompt enrollmentPrompt, errOut io.Writer) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var err error
		retry, replace := true, true
		if len(token) != 64 {
			err = fmt.Errorf("注册令牌格式错误：应为 64 个字符")
		} else {
			err = attempt(ctx, token)
			retry, replace = connection.EnrollmentRetry(err)
		}
		if err == nil {
			return nil
		}
		fmt.Fprintln(errOut, err)
		if ctx.Err() != nil || prompt == nil || !retry || !prompt.Retry(replace) {
			return err
		}
		if replace {
			token, err = prompt.Token()
			if err != nil {
				fmt.Fprintln(errOut, err)
				return err
			}
		}
	}
}
