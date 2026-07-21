package installer

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	sshx "aksu-installer/internal/ssh"
)

// MinIO 安装器：按架构上传二进制 -> 建用户/目录 -> 写配置（端口/账号变量）->
// systemd -> 启动 -> 健康检查。对应手册第 5 节。
type MinIO struct{}

func (m *MinIO) Name() string { return "MinIO" }

func (m *MinIO) Install(c *sshx.Client, p Params, offlineRoot string) error {
	arch, err := DetectArch(c)
	if err != nil {
		return err
	}
	localBin := filepath.Join(offlineRoot, "middleware", arch, "minio", "minio")
	minioHome := path.Join(p.InstallBase, "minio")

	// 1. 目录 + 上传二进制
	if err := c.Run(fmt.Sprintf("mkdir -p %s/bin %s/etc %s/data", minioHome, minioHome, minioHome)); err != nil {
		return err
	}
	// 先停旧服务再覆盖二进制（幂等重装）
	_ = c.Run("systemctl stop minio 2>/dev/null || true")
	if err := c.Upload(localBin, path.Join(minioHome, "bin", "minio")); err != nil {
		return err
	}
	if err := c.Run("chmod +x " + path.Join(minioHome, "bin", "minio")); err != nil {
		return err
	}
	if err := c.Run(path.Join(minioHome, "bin", "minio") + " -version | head -2"); err != nil {
		return fmt.Errorf("minio 二进制无法执行（架构不匹配？）: %w", err)
	}

	// 2. 用户
	if err := c.Run("getent group minio >/dev/null || groupadd minio; id minio >/dev/null 2>&1 || useradd -r -g minio -s /sbin/nologin minio || useradd -r -g minio -s /usr/sbin/nologin minio"); err != nil {
		return err
	}

	// 3. 配置文件（手册 5.5，端口和账号做成变量）
	conf := strings.Join([]string{
		`MINIO_VOLUMES="` + minioHome + `/data"`,
		fmt.Sprintf(`MINIO_OPTS="-C %s/etc --address 0.0.0.0:%s --console-address 0.0.0.0:%s"`, minioHome, p.MinioApiPort, p.MinioConsolePort),
		`MINIO_ROOT_USER="` + p.MinioRootUser + `"`,
		`MINIO_ROOT_PASSWORD="` + p.MinioRootPass + `"`,
	}, "\\n")
	if err := c.Run(fmt.Sprintf("printf '%s\\n' > %s/etc/minio.conf && chmod 600 %s/etc/minio.conf", conf, minioHome, minioHome)); err != nil {
		return err
	}

	// 4. systemd（手册 5.6）
	unit := strings.Join([]string{
		"[Unit]",
		"Description=MinIO",
		"Wants=network-online.target",
		"After=network-online.target",
		"AssertFileIsExecutable=" + minioHome + "/bin/minio",
		"",
		"[Service]",
		"WorkingDirectory=" + minioHome,
		"User=minio",
		"Group=minio",
		"EnvironmentFile=-" + minioHome + "/etc/minio.conf",
		"ExecStart=" + minioHome + "/bin/minio server $MINIO_OPTS $MINIO_VOLUMES",
		"Restart=on-failure",
		"RestartSec=5",
		"",
		"[Install]",
		"WantedBy=multi-user.target",
	}, "\\n")
	if err := c.Run(fmt.Sprintf("printf '%s\\n' > /etc/systemd/system/minio.service", unit)); err != nil {
		return err
	}

	// 5. 授权并启动
	if err := c.Run("chown -R minio:minio " + minioHome); err != nil {
		return err
	}
	if err := c.Run("systemctl daemon-reload && systemctl enable minio >/dev/null 2>&1; systemctl restart minio"); err != nil {
		return fmt.Errorf("MinIO 启动失败: %w", err)
	}

	// 6. 健康检查（/minio/health/live 返回 200）
	if err := c.Run(fmt.Sprintf("sleep 3 && curl -sf -o /dev/null http://127.0.0.1:%s/minio/health/live && echo 'MinIO 健康检查通过'", p.MinioApiPort)); err != nil {
		_ = c.Run("journalctl -u minio -n 15 --no-pager")
		return fmt.Errorf("MinIO 健康检查失败: %w", err)
	}
	c.Run(fmt.Sprintf("echo 'MinIO 安装完成：API http://本机IP:%s  控制台 http://本机IP:%s  账号 %s'", p.MinioApiPort, p.MinioConsolePort, p.MinioRootUser))
	return nil
}

// Cleanup 停服务、删 unit/目录/用户。
func (m *MinIO) Cleanup(c *sshx.Client, p Params) error {
	return c.Run(fmt.Sprintf(`systemctl stop minio 2>/dev/null; systemctl disable minio 2>/dev/null; rm -f /etc/systemd/system/minio.service; systemctl daemon-reload; rm -rf %s/minio; id minio >/dev/null 2>&1 && userdel minio 2>/dev/null; getent group minio >/dev/null && groupdel minio 2>/dev/null; echo '[清理] MinIO 痕迹已删除'`, p.InstallBase))
}
