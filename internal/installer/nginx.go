package installer

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	sshx "aksu-installer/internal/ssh"
)

// Nginx 安装器：上传源码 -> 检测编译工具链 -> configure+make 装到 /data/apps/nginx
// -> systemd -> 验证。对应手册第 3 节。无网络需目标机有 gcc + pcre/zlib/openssl-devel。
type Nginx struct{}

func (n *Nginx) Name() string { return "Nginx" }

const nginxTarball = "nginx-1.26.1.tar.gz"

func (n *Nginx) Install(c *sshx.Client, p Params, offlineRoot string) error {
	nginxHome := path.Join(p.InstallBase, "nginx")
	localPath := filepath.Join(offlineRoot, "middleware", "common", "nginx", nginxTarball)
	remoteTar := path.Join(p.TmpDir, nginxTarball)
	srcDir := path.Join(p.TmpDir, "nginx-1.26.1")

	// 1. 编译工具链（gcc/make + pcre/zlib/openssl 头文件）
	if _, err := c.Output("command -v gcc >/dev/null && command -v make >/dev/null && echo ok"); err != nil {
		c.Run("echo '未检测到 gcc/make，尝试用系统包管理器安装（无网络/无本地源会失败）...'")
		if pm, e := DetectPkgManager(c); e == nil {
			if pm == "apt-get" {
				_ = c.Run("apt-get install -y gcc make libpcre3-dev zlib1g-dev libssl-dev")
			} else {
				_ = c.Run(pm + " install -y gcc make pcre pcre-devel zlib zlib-devel openssl openssl-devel")
			}
		}
		if _, err := c.Output("command -v gcc >/dev/null && command -v make >/dev/null && echo ok"); err != nil {
			return fmt.Errorf("缺少 gcc/make 且无法自动安装。无网络请挂系统 ISO 配本地源装 gcc pcre-devel zlib-devel openssl-devel（见《无网络部署依赖清单》）")
		}
	}

	// 2. 上传 + 解压 + 编译
	if err := c.Run("mkdir -p " + p.TmpDir); err != nil {
		return err
	}
	if err := c.Upload(localPath, remoteTar); err != nil {
		return err
	}
	if err := c.Run(fmt.Sprintf("rm -rf %s && tar -xf %s -C %s", srcDir, remoteTar, p.TmpDir)); err != nil {
		return err
	}
	c.Run("echo '编译 Nginx（约 1-2 分钟）...'")
	conf := fmt.Sprintf("cd %s && ./configure --prefix=%s --with-http_stub_status_module --with-http_ssl_module --with-stream >/tmp/nginx_cfg.log 2>&1 || (tail -20 /tmp/nginx_cfg.log; exit 1)", srcDir, nginxHome)
	if err := c.Run(conf); err != nil {
		return fmt.Errorf("Nginx configure 失败（多半缺 pcre-devel/openssl-devel）: %w", err)
	}
	if err := c.Run(fmt.Sprintf("cd %s && make -j$(nproc) >/tmp/nginx_make.log 2>&1 && make install >/dev/null || (tail -20 /tmp/nginx_make.log; exit 1)", srcDir)); err != nil {
		return fmt.Errorf("Nginx 编译失败: %w", err)
	}

	// 3. 端口非 80 时改默认 server 的 listen
	if p.NginxPort != "80" {
		_ = c.Run(fmt.Sprintf("sed -i 's/listen\\s*80;/listen %s;/' %s/conf/nginx.conf", p.NginxPort, nginxHome))
	}
	_ = c.Run(fmt.Sprintf("mkdir -p %s/conf/conf.d && ln -sf %s/sbin/nginx /usr/bin/nginx", nginxHome, nginxHome))

	// 4. systemd（手册 3.6）
	unit := strings.Join([]string{
		"[Unit]",
		"Description=nginx",
		"After=network.target remote-fs.target nss-lookup.target",
		"",
		"[Service]",
		"Type=forking",
		"PIDFile=" + nginxHome + "/logs/nginx.pid",
		"ExecStartPre=" + nginxHome + "/sbin/nginx -t -c " + nginxHome + "/conf/nginx.conf",
		"ExecStart=" + nginxHome + "/sbin/nginx -c " + nginxHome + "/conf/nginx.conf",
		"ExecReload=/bin/kill -s HUP $MAINPID",
		"ExecStop=/bin/kill -s QUIT $MAINPID",
		"PrivateTmp=true",
		"",
		"[Install]",
		"WantedBy=multi-user.target",
	}, "\\n")
	if err := c.Run(fmt.Sprintf("printf '%s\\n' > /usr/lib/systemd/system/nginx.service", unit)); err != nil {
		return err
	}
	if err := c.Run("systemctl daemon-reload && systemctl enable nginx >/dev/null 2>&1; systemctl restart nginx"); err != nil {
		return fmt.Errorf("Nginx 启动失败: %w", err)
	}

	_ = c.Run(fmt.Sprintf("rm -rf %s %s", remoteTar, srcDir))
	check := fmt.Sprintf(`sleep 1 && curl -s -o /dev/null -w '%%{http_code}' http://127.0.0.1:%s/ 2>/dev/null | grep -qE '200|403|404' && echo 'Nginx 验证通过' || (%s/sbin/nginx -V 2>&1 | head -1)`, p.NginxPort, nginxHome)
	if err := c.Run(check); err != nil {
		return fmt.Errorf("Nginx 验证失败: %w", err)
	}
	c.Run(fmt.Sprintf("echo 'Nginx 安装完成：端口 %s，配置 %s/conf/nginx.conf（含 conf.d/），软链 /usr/bin/nginx'", p.NginxPort, nginxHome))
	return nil
}

func (n *Nginx) Cleanup(c *sshx.Client, p Params) error {
	return c.Run(fmt.Sprintf(`systemctl stop nginx 2>/dev/null; systemctl disable nginx 2>/dev/null; rm -f /usr/lib/systemd/system/nginx.service /usr/bin/nginx; systemctl daemon-reload; rm -rf %s/nginx; echo '[清理] Nginx 已删除'`, p.InstallBase))
}
