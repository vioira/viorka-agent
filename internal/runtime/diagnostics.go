package runtime

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"syscall"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

type diagnosticKey struct{}

// Diagnostics belongs to one serial engine task. Persist runs before each
// phase's effects; only structured, credential-free reasons enter the reply.
type Diagnostics struct {
	stage   string
	failure *v1.EngineDiagnostic
	persist func(string) error
}

func TrackDiagnostics(ctx context.Context, persist func(string) error) (context.Context, *Diagnostics) {
	d := &Diagnostics{stage: "prepare", persist: persist}
	return context.WithValue(ctx, diagnosticKey{}, d), d
}

func checkpoint(ctx context.Context, stage string) error {
	d, _ := ctx.Value(diagnosticKey{}).(*Diagnostics)
	if d == nil || d.stage == stage {
		return nil
	}
	if err := d.persist(stage); err != nil {
		d.failure = &v1.EngineDiagnostic{Stage: d.stage, Message: "无法保存任务执行阶段：" + safeFailure(err)}
		return err
	}
	d.stage = stage
	return nil
}

func reportReason(ctx context.Context, message string, status *uint64) {
	d, _ := ctx.Value(diagnosticKey{}).(*Diagnostics)
	if d != nil {
		d.failure = &v1.EngineDiagnostic{Stage: d.stage, Message: message, HttpStatus: status}
	}
}

func reportFailure(ctx context.Context, err error) {
	if err == nil {
		return
	}
	reportReason(ctx, safeFailure(err), nil)
	d, _ := ctx.Value(diagnosticKey{}).(*Diagnostics)
	var exit *exec.ExitError
	if d != nil && errors.As(err, &exit) && exit.ExitCode() >= 0 && exit.ExitCode() <= 255 {
		code := uint64(exit.ExitCode())
		d.failure.ExitCode = &code
	}
}

func safeFailure(err error) string {
	var exit *exec.ExitError
	var network net.Error
	var cert x509.UnknownAuthorityError
	var hostname x509.HostnameError
	switch {
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		return "磁盘空间或配额不足"
	case errors.Is(err, os.ErrPermission):
		return "权限不足，无法完成操作"
	case errors.Is(err, os.ErrNotExist):
		return "所需文件或程序不存在"
	case errors.Is(err, context.DeadlineExceeded):
		return "操作超过截止时间"
	case errors.Is(err, context.Canceled):
		return "操作被取消"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "数据传输或归档内容不完整"
	case errors.As(err, &cert), errors.As(err, &hostname):
		return "TLS 证书验证失败"
	case errors.As(err, &exit):
		if exit.ExitCode() < 0 {
			return "外部程序被信号终止"
		}
		return "外部程序执行失败"
	case errors.As(err, &network):
		if network.Timeout() {
			return "网络请求超时"
		}
		return "网络连接或域名解析失败"
	default:
		return "操作失败，请查看节点运行日志"
	}
}

func (d *Diagnostics) Result(code string) *v1.EngineDiagnostic {
	if d.failure != nil {
		return d.failure
	}
	message := map[string]string{
		"engine_unsupported":        "当前环境或引擎状态不支持此操作",
		"engine_external":           "发现第三方实例或资源归属不一致",
		"engine_download_failed":    "引擎下载或解压失败",
		"engine_checksum":           "下载文件的 SHA-256 与发行清单不一致",
		"engine_invalid_config":     "配置校验失败",
		"engine_target_unreachable": "目标地址、端口或 TLS 预检失败",
		"engine_port_conflict":      "本地监听端口已被占用",
		"engine_start_failed":       "引擎启动或健康检查失败",
		"engine_rollback_failed":    "恢复上一版本失败",
		"engine_io_failed":          "文件或服务操作失败",
		"engine_result_unknown":     "执行结果无法确认",
		"deadline_exceeded":         "任务已超过截止时间",
	}[code]
	if message == "" {
		message = "任务执行失败"
	}
	return &v1.EngineDiagnostic{Stage: d.stage, Message: message}
}
