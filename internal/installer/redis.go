package installer

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	sshx "aksu-installer/internal/ssh"
)

// Redis 安装器：上传源码包 -> 装编译依赖 -> make 安装到 /data/apps/redis ->
// 按手册配置（密码、持久化、systemd、redis 用户）-> 启动 -> auth 验证。
// 对应手册第 4 节；jemalloc 用源码自带的，不依赖 epel。
type Redis struct{}

func (r *Redis) Name() string { return "Redis" }

const redisTarball = "redis-6.2.9.tar.gz"

func (r *Redis) Install(c *sshx.Client, p Params, offlineRoot string) error {
	redisHome := path.Join(p.InstallBase, "redis")
	localPath := filepath.Join(offlineRoot, "middleware", "common", "redis", redisTarball)
	remoteTar := path.Join(p.TmpDir, redisTarball)
	srcDir := path.Join(p.TmpDir, "redis-6.2.9")

	// 1. 编译工具链：无网络环境必须预装。先检测，已有则直接用，
	//    缺失才尝试包管理器安装（无网络会失败，给清晰提示）。
	if _, err := c.Output("command -v gcc >/dev/null && command -v make >/dev/null && echo ok"); err != nil {
		c.Run("echo '未检测到 gcc/make，尝试用系统包管理器安装（无网络/无本地源会失败）...'")
		pm, pmErr := DetectPkgManager(c)
		if pmErr == nil {
			var depCmd string
			if pm == "apt-get" {
				depCmd = "apt-get install -y gcc make pkg-config"
			} else {
				depCmd = pm + " install -y gcc gcc-c++ make"
			}
			_ = c.Run(depCmd)
		}
		if _, err := c.Output("command -v gcc >/dev/null && command -v make >/dev/null && echo ok"); err != nil {
			return fmt.Errorf("缺少 gcc/make 且无法自动安装。无网络环境请先准备编译工具链（见《无网络部署依赖清单》），或用系统安装盘配本地 yum 源")
		}
	}

	// 2. 上传 + 解压 + 编译（自带 jemalloc）
	if err := c.Run("mkdir -p " + p.TmpDir); err != nil {
		return err
	}
	if err := c.Upload(localPath, remoteTar); err != nil {
		return err
	}
	if err := c.Run(fmt.Sprintf("rm -rf %s && tar -xf %s -C %s", srcDir, remoteTar, p.TmpDir)); err != nil {
		return err
	}
	c.Run("echo '开始编译 Redis（约 1-3 分钟）...'")
	if err := c.Run(fmt.Sprintf("cd %s && make -j$(nproc) >/tmp/redis_make.log 2>&1 || (tail -30 /tmp/redis_make.log; exit 1)", srcDir)); err != nil {
		return fmt.Errorf("Redis 编译失败: %w", err)
	}
	if err := c.Run(fmt.Sprintf("cd %s && make PREFIX=%s install >/dev/null", srcDir, redisHome)); err != nil {
		return err
	}

	// 3. 用户与目录
	if err := c.Run("getent group redis >/dev/null || groupadd redis; id redis >/dev/null 2>&1 || useradd -r -g redis -s /sbin/nologin redis || useradd -r -g redis -s /usr/sbin/nologin redis"); err != nil {
		return err
	}
	if err := c.Run(fmt.Sprintf("mkdir -p %s/logs %s/etc %s/data", redisHome, redisHome, redisHome)); err != nil {
		return err
	}

	// 4. 生成配置（手册 4.5/4.6/4.7 合并：密码、systemd 托管、持久化）。
	//    daemonize no + Type=simple：redis 前台运行由 systemd 直接托管，
	//    不用 notify（那需要编译时链接 libsystemd）。
	conf := strings.Join([]string{
		"port " + p.RedisPort,
		"protected-mode no",
		"daemonize no",
		"supervised no",
		"requirepass " + p.RedisPassword,
		"pidfile " + redisHome + "/logs/redis.pid",
		`logfile "` + redisHome + `/logs/redis.log"`,
		"dir " + redisHome + "/data",
		"save 900 1",
		"save 300 10",
		"save 60 10000",
		"dbfilename dump.rdb",
		"appendonly yes",
		`appendfilename "appendonly.aof"`,
		"appendfsync everysec",
		"aof-use-rdb-preamble yes",
	}, "\\n")
	if err := c.Run(fmt.Sprintf("printf '%s\\n' > %s/etc/redis.conf && chmod 640 %s/etc/redis.conf", conf, redisHome, redisHome)); err != nil {
		return err
	}
	if err := c.Run("chown -R redis:redis " + redisHome); err != nil {
		return err
	}

	// 5. systemd 服务
	unit := strings.Join([]string{
		"[Unit]",
		"Description=Redis persistent key-value database",
		"After=network.target",
		"",
		"[Service]",
		"Type=simple",
		"User=redis",
		"Group=redis",
		"ExecStart=" + redisHome + "/bin/redis-server " + redisHome + "/etc/redis.conf",
		"Restart=on-failure",
		"",
		"[Install]",
		"WantedBy=multi-user.target",
	}, "\\n")
	if err := c.Run(fmt.Sprintf("printf '%s\\n' > /etc/systemd/system/redis.service", unit)); err != nil {
		return err
	}
	if err := c.Run("systemctl daemon-reload && systemctl enable redis >/dev/null 2>&1; systemctl restart redis"); err != nil {
		return fmt.Errorf("Redis 启动失败: %w", err)
	}

	// 6. 清理 + 验证（auth 后 ping 应回 PONG）
	_ = c.Run(fmt.Sprintf("rm -rf %s %s", remoteTar, srcDir))
	if err := c.Run(fmt.Sprintf("sleep 1 && %s/bin/redis-cli -p %s -a '%s' ping 2>/dev/null | grep -q PONG && echo 'Redis 验证通过: PONG'", redisHome, p.RedisPort, p.RedisPassword)); err != nil {
		return fmt.Errorf("Redis 验证失败: %w", err)
	}
	c.Run(fmt.Sprintf("echo 'Redis 安装完成：端口 %s，配置 %s/etc/redis.conf，systemctl status redis'", p.RedisPort, redisHome))
	return nil
}

// Cleanup 停服务、删 unit/目录/用户。
func (r *Redis) Cleanup(c *sshx.Client, p Params) error {
	return c.Run(fmt.Sprintf(`systemctl stop redis 2>/dev/null; systemctl disable redis 2>/dev/null; rm -f /etc/systemd/system/redis.service; systemctl daemon-reload; rm -rf %s/redis; id redis >/dev/null 2>&1 && userdel redis 2>/dev/null; getent group redis >/dev/null && groupdel redis 2>/dev/null; echo '[清理] Redis 痕迹已删除'`, p.InstallBase))
}
