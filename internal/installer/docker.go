package installer

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	sshx "aksu-installer/internal/ssh"
)

// Docker 安装器：静态二进制离线安装（不依赖 yum/apt 源，发行版通用）。
// 按架构上传 tgz -> 解压到 /usr/local/bin -> daemon.json（数据目录按手册
// 放 /data/apps/docker，可配私有镜像仓库）-> systemd -> 验证。
// 已装 docker 且在运行时跳过（只调整 daemon.json 需人工，避免弄坏现有环境）。
type Docker struct{}

func (d *Docker) Name() string { return "Docker" }

const (
	dockerTgz       = "docker-29.5.3.tgz"
	dockerTgzLegacy = "docker-20.10.23.tgz" // 内核 < 4 的老系统（CentOS 7 等）
)

func (d *Docker) Install(c *sshx.Client, p Params, offlineRoot string) error {
	// 0. 机器上已有 docker（无论是否在运行）一律跳过，避免覆盖已有安装
	//    （daemon.json 数据目录一旦被改，原有容器会"消失"）。
	if _, err := c.Output("command -v docker"); err == nil {
		ver, _ := c.Output("docker --version 2>/dev/null")
		state, _ := c.Output("systemctl is-active docker 2>/dev/null")
		c.Run(fmt.Sprintf("echo 'Docker 已存在（%s，状态 %s），跳过安装。如确需重装请先人工卸载。'", ver, state))
		return nil
	}

	arch, err := DetectArch(c)
	if err != nil {
		return err
	}
	// 老内核（CentOS 7 = 3.10）用 20.10 老版本，新内核用 29.x
	kernelMajor := DetectOS(c)
	tgz := dockerTgz
	if kernelMajor > 0 && kernelMajor < 4 {
		tgz = dockerTgzLegacy
		c.Run("echo '内核版本 < 4（如 CentOS 7），自动选用 Docker 20.10 兼容版'")
	}
	localTgz := filepath.Join(offlineRoot, "docker", arch, tgz)
	remoteTgz := path.Join(p.TmpDir, tgz)
	dataRoot := path.Join(p.InstallBase, "docker")

	// 1. 上传 + 解压到 /usr/local/bin
	if err := c.Run("mkdir -p " + p.TmpDir); err != nil {
		return err
	}
	if err := c.Upload(localTgz, remoteTgz); err != nil {
		return err
	}
	if err := c.Run(fmt.Sprintf("tar -xzf %s -C %s && cp -f %s/docker/* /usr/local/bin/ && rm -rf %s/docker %s", remoteTgz, p.TmpDir, p.TmpDir, p.TmpDir, remoteTgz)); err != nil {
		return err
	}
	if err := c.Run("/usr/local/bin/docker --version && /usr/local/bin/dockerd --version"); err != nil {
		return fmt.Errorf("docker 二进制无法执行（架构不匹配？）: %w", err)
	}

	// 2. daemon.json：数据目录 + 镜像源（私有仓库优先，手册公共源兜底）
	mirrors := []string{}
	insecure := ""
	if m := strings.TrimSpace(p.DockerMirror); m != "" {
		mirrors = append(mirrors, `"`+m+`"`)
		// http 私有仓库需要 insecure-registries
		host := strings.TrimPrefix(strings.TrimPrefix(m, "http://"), "https://")
		if strings.HasPrefix(m, "http://") {
			insecure = fmt.Sprintf(`,\n  "insecure-registries": ["%s"]`, host)
		}
	}
	for _, m := range []string{"https://docker.1ms.run", "https://docker.m.daocloud.io", "https://docker.1panel.live"} {
		mirrors = append(mirrors, `"`+m+`"`)
	}
	daemonJSON := fmt.Sprintf(`{\n  "data-root": "%s",\n  "registry-mirrors": [%s]%s\n}`, dataRoot, strings.Join(mirrors, ", "), insecure)
	if err := c.Run(fmt.Sprintf("mkdir -p /etc/docker %s && printf '%s\\n' > /etc/docker/daemon.json", dataRoot, daemonJSON)); err != nil {
		return err
	}

	// 3. systemd（dockerd 未指定外部 containerd 时会自动拉起自带的）
	unit := strings.Join([]string{
		"[Unit]",
		"Description=Docker Application Container Engine",
		"After=network-online.target",
		"Wants=network-online.target",
		"",
		"[Service]",
		"Type=notify",
		"ExecStart=/usr/local/bin/dockerd",
		"ExecReload=/bin/kill -s HUP $MAINPID",
		"Restart=always",
		"RestartSec=5",
		"LimitNOFILE=1048576",
		"LimitNPROC=infinity",
		"Delegate=yes",
		"KillMode=process",
		"",
		"[Install]",
		"WantedBy=multi-user.target",
	}, "\\n")
	if err := c.Run(fmt.Sprintf("printf '%s\\n' > /etc/systemd/system/docker.service", unit)); err != nil {
		return err
	}
	_ = c.Run("getent group docker >/dev/null || groupadd docker")
	if err := c.Run("systemctl daemon-reload && systemctl enable docker >/dev/null 2>&1; systemctl restart docker"); err != nil {
		_ = c.Run("journalctl -u docker -n 20 --no-pager")
		return fmt.Errorf("Docker 启动失败: %w", err)
	}

	// 4. 验证：daemon 可用 + 数据目录生效
	if err := c.Run("sleep 2 && docker info --format 'Server={{.ServerVersion}} DataRoot={{.DockerRootDir}}' 2>/dev/null || /usr/local/bin/docker info | grep -E 'Server Version|Docker Root Dir'"); err != nil {
		return fmt.Errorf("Docker 验证失败: %w", err)
	}
	c.Run(fmt.Sprintf("echo 'Docker 安装完成：数据目录 %s，镜像源已配置（私有仓库可改 /etc/docker/daemon.json 后 systemctl restart docker）'", dataRoot))
	return nil
}

// Cleanup 只清理本工具装的静态版（以 /usr/local/bin/dockerd 为标记），
// 绝不碰发行版包管理器装的 docker。
func (d *Docker) Cleanup(c *sshx.Client, p Params) error {
	return c.Run(fmt.Sprintf(`if [ -x /usr/local/bin/dockerd ]; then systemctl stop docker 2>/dev/null; systemctl disable docker 2>/dev/null; rm -f /etc/systemd/system/docker.service; systemctl daemon-reload; rm -f /usr/local/bin/docker /usr/local/bin/dockerd /usr/local/bin/docker-init /usr/local/bin/docker-proxy /usr/local/bin/containerd* /usr/local/bin/ctr /usr/local/bin/runc; rm -f /etc/docker/daemon.json; rm -rf %s/docker; echo '[清理] Docker(静态版) 痕迹已删除'; else echo '[清理] 未发现本工具安装的 Docker，跳过'; fi`, p.InstallBase))
}
