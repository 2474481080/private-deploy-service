package installer

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	sshx "aksu-installer/internal/ssh"
)

// Sentinel 安装器：上传 dashboard jar -> systemd 托管 -> HTTP 检查。
// 对应手册第 6 节（rc.local + restart.sh 改为 systemd）。依赖 JDK。
type Sentinel struct{}

func (s *Sentinel) Name() string { return "Sentinel" }

const sentinelJar = "sentinel-dashboard-1.8.2.jar"

func (s *Sentinel) Install(c *sshx.Client, p Params, offlineRoot string) error {
	home := path.Join(p.InstallBase, "sentinel")
	jdkLink := path.Join(p.InstallBase, "jdk")
	if _, err := c.Output("test -x " + jdkLink + "/bin/java && echo ok"); err != nil {
		return fmt.Errorf("未找到 %s/bin/java，请先安装 JDK", jdkLink)
	}

	if err := c.Run("mkdir -p " + home); err != nil {
		return err
	}
	_ = c.Run("systemctl stop sentinel 2>/dev/null || true")
	if err := c.Upload(filepath.Join(offlineRoot, "middleware", "common", "sentinel", sentinelJar), path.Join(home, sentinelJar)); err != nil {
		return err
	}

	unit := strings.Join([]string{
		"[Unit]",
		"Description=Sentinel Dashboard",
		"After=network.target",
		"",
		"[Service]",
		"Type=simple",
		"WorkingDirectory=" + home,
		fmt.Sprintf("ExecStart=%s/bin/java -jar %s/%s --server.port=%s", jdkLink, home, sentinelJar, p.SentinelPort),
		"Restart=on-failure",
		"RestartSec=10",
		"",
		"[Install]",
		"WantedBy=multi-user.target",
	}, "\\n")
	if err := c.Run(fmt.Sprintf("printf '%s\\n' > /etc/systemd/system/sentinel.service", unit)); err != nil {
		return err
	}
	if err := c.Run("systemctl daemon-reload && systemctl enable sentinel >/dev/null 2>&1; systemctl restart sentinel"); err != nil {
		return fmt.Errorf("Sentinel 启动失败: %w", err)
	}

	check := fmt.Sprintf(`for i in $(seq 1 20); do code=$(curl -s -o /dev/null -w '%%{http_code}' http://127.0.0.1:%s/ 2>/dev/null); case "$code" in 200|302) echo "Sentinel 检查通过 (HTTP $code)"; exit 0;; esac; sleep 3; done; echo 检查超时; journalctl -u sentinel -n 15 --no-pager; exit 1`, p.SentinelPort)
	if err := c.Run(check); err != nil {
		return fmt.Errorf("Sentinel 检查失败: %w", err)
	}
	c.Run(fmt.Sprintf("echo 'Sentinel 安装完成：http://本机IP:%s（默认账号 sentinel/sentinel）'", p.SentinelPort))
	return nil
}

// Cleanup 停服务、删 unit/目录。
func (s *Sentinel) Cleanup(c *sshx.Client, p Params) error {
	return c.Run(fmt.Sprintf(`systemctl stop sentinel 2>/dev/null; systemctl disable sentinel 2>/dev/null; rm -f /etc/systemd/system/sentinel.service; systemctl daemon-reload; rm -rf %s/sentinel; echo '[清理] Sentinel 痕迹已删除'`, p.InstallBase))
}
