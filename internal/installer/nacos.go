package installer

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	sshx "aksu-installer/internal/ssh"
)

// Nacos 安装器：上传 zip -> 解压到 /data/apps/nacos -> 按数据库模式写配置
// （达梦：放 dm 驱动+插件、写连接串；内嵌：Nacos 自带 derby，用于无达梦的验证）
// -> systemd 托管 standalone -> HTTP 就绪检查。
// 对应手册第 8 节。达梦建表 SQL 无法远程自动执行，安装完提示人工导入。
// 依赖 JDK（/data/apps/jdk 软链），批量安装时 InstallOrder 保证 JDK 先装。
type Nacos struct{}

func (n *Nacos) Name() string { return "Nacos" }

const nacosZip = "nacos-server-2.5.1.zip"

func (n *Nacos) Install(c *sshx.Client, p Params, offlineRoot string) error {
	nacosHome := path.Join(p.InstallBase, "nacos")
	jdkLink := path.Join(p.InstallBase, "jdk")
	base := filepath.Join(offlineRoot, "middleware", "common", "nacos")

	// 0. 前置：JDK 必须已装（软链存在）
	if _, err := c.Output("test -x " + jdkLink + "/bin/java && echo ok"); err != nil {
		return fmt.Errorf("未找到 %s/bin/java，请先安装 JDK（勾选 JDK 组件或提前安装）", jdkLink)
	}

	// 1. 上传 + 解压（无网络环境用 JDK 自带的 jar 解 zip，不依赖 unzip）
	if err := c.Run("mkdir -p " + p.TmpDir); err != nil {
		return err
	}
	remoteZip := path.Join(p.TmpDir, nacosZip)
	if err := c.Upload(filepath.Join(base, nacosZip), remoteZip); err != nil {
		return err
	}
	_ = c.Run("systemctl stop nacos 2>/dev/null || true")
	// 优先 unzip，没有则用 jar xf（JDK 已装，jar 一定存在）
	extract := fmt.Sprintf("if command -v unzip >/dev/null 2>&1; then unzip -q %s -d %s/nacos_extract; else (cd %s/nacos_extract && %s/bin/jar xf %s); fi",
		remoteZip, p.TmpDir, p.TmpDir, jdkLink, remoteZip)
	if err := c.Run(fmt.Sprintf("rm -rf %s %s/nacos_extract && mkdir -p %s/nacos_extract && %s && mv %s/nacos_extract/nacos %s && rm -rf %s/nacos_extract",
		nacosHome, p.TmpDir, p.TmpDir, extract, p.TmpDir, nacosHome, p.TmpDir)); err != nil {
		return fmt.Errorf("解压 nacos 失败: %w", err)
	}

	// 2. 数据库模式：dameng（上传驱动+插件）| mysql（Nacos 内置驱动）| 内嵌 derby
	useDB := strings.TrimSpace(p.NacosDmHost) != ""
	var dbConf string
	if useDB && p.NacosDbType == "mysql" {
		dbConf = strings.Join([]string{
			"spring.datasource.platform=mysql",
			"db.num=1",
			fmt.Sprintf("db.url.0=jdbc:mysql://%s:%s/%s?characterEncoding=utf8&connectTimeout=1000&socketTimeout=3000&autoReconnect=true&useUnicode=true&useSSL=false&serverTimezone=Asia/Shanghai",
				p.NacosDmHost, p.NacosDmPort, p.NacosDmSchema),
			"db.user.0=" + p.NacosDmUser,
			"db.password.0=" + p.NacosDmPass,
		}, "\\n")
		c.Run("echo '数据库模式：MySQL " + p.NacosDmHost + ":" + p.NacosDmPort + " 库=" + p.NacosDmSchema + "（建表 SQL 在 nacos/conf/mysql-schema.sql）'")
	} else if useDB {
		// 达梦驱动 + dm8 插件
		if err := c.Run("mkdir -p " + nacosHome + "/plugins"); err != nil {
			return err
		}
		for _, jar := range []string{"DmJdbcDriver8.jar", "nacos-plugin-dm8-2.5.1.jar"} {
			if err := c.Upload(filepath.Join(base, "plugins", jar), path.Join(nacosHome, "plugins", jar)); err != nil {
				return err
			}
		}
		dbConf = strings.Join([]string{
			"spring.datasource.platform=dameng",
			"db.num=1",
			fmt.Sprintf("db.url.0=jdbc:dm://%s:%s/%s?schema=%s&compatibleMode=mysql&ignoreCase=true&ENCODING=utf-8",
				p.NacosDmHost, p.NacosDmPort, p.NacosDmSchema, p.NacosDmSchema),
			"db.user=" + p.NacosDmUser,
			"db.password=" + p.NacosDmPass,
			"db.pool.config.driverClassName=dm.jdbc.driver.DmDriver",
		}, "\\n")
		c.Run("echo '数据库模式：达梦 " + p.NacosDmHost + ":" + p.NacosDmPort + " schema=" + p.NacosDmSchema + "'")
	} else {
		dbConf = "# 未配置数据库，使用 Nacos 内嵌数据库（derby，仅用于测试验证）"
		c.Run("echo '数据库模式：内嵌 derby（未填数据库地址；生产请填达梦/MySQL 并重装）'")
	}

	// 3. 追加配置到 application.properties（鉴权按手册开启）
	extra := strings.Join([]string{
		"", "### aksu-installer 追加配置",
		dbConf,
		"nacos.core.auth.system.type=nacos",
		"nacos.core.auth.enabled=true",
		"nacos.core.auth.server.identity.key=nacos",
		"nacos.core.auth.server.identity.value=nacos",
		"nacos.core.auth.plugin.nacos.token.secret.key=" + p.NacosTokenSecret,
		"nacos.core.param.check.enabled=false",
	}, "\\n")
	if err := c.Run(fmt.Sprintf("printf '%s\\n' >> %s/conf/application.properties", extra, nacosHome)); err != nil {
		return err
	}
	// 端口非默认时替换
	if p.NacosPort != "8848" {
		if err := c.Run(fmt.Sprintf("sed -i 's/^server.port=8848/server.port=%s/' %s/conf/application.properties", p.NacosPort, nacosHome)); err != nil {
			return err
		}
	}

	// 4. systemd（startup.sh 是后台拉起 java，用 forking）
	unit := strings.Join([]string{
		"[Unit]",
		"Description=Nacos Server (standalone)",
		"After=network.target",
		"",
		"[Service]",
		"Type=forking",
		"Environment=JAVA_HOME=" + jdkLink,
		"ExecStart=/bin/bash " + nacosHome + "/bin/startup.sh -m standalone",
		"ExecStop=/bin/bash " + nacosHome + "/bin/shutdown.sh",
		"Restart=on-failure",
		"RestartSec=10",
		"TimeoutStartSec=180",
		"",
		"[Install]",
		"WantedBy=multi-user.target",
	}, "\\n")
	if err := c.Run(fmt.Sprintf("printf '%s\\n' > /etc/systemd/system/nacos.service", unit)); err != nil {
		return err
	}
	if err := c.Run("systemctl daemon-reload && systemctl enable nacos >/dev/null 2>&1; systemctl restart nacos"); err != nil {
		_ = c.Run("tail -30 " + nacosHome + "/logs/start.out 2>/dev/null")
		return fmt.Errorf("Nacos 启动失败: %w", err)
	}

	// 5. 清理 + 就绪检查（最多等 90 秒）
	_ = c.Run("rm -f " + remoteZip)
	check := fmt.Sprintf(`for i in $(seq 1 30); do code=$(curl -s -o /dev/null -w '%%{http_code}' http://127.0.0.1:%s/nacos/v1/console/health/readiness 2>/dev/null); [ "$code" = "200" ] && echo "Nacos 就绪检查通过" && exit 0; sleep 3; done; echo "就绪检查超时"; tail -20 %s/logs/start.out 2>/dev/null; exit 1`, p.NacosPort, nacosHome)
	if err := c.Run(check); err != nil {
		return fmt.Errorf("Nacos 就绪检查失败: %w", err)
	}

	c.Run(fmt.Sprintf("echo 'Nacos 安装完成：http://本机IP:%s/nacos（默认账号 nacos/nacos，登录后请改密码）'", p.NacosPort))
	if useDB && p.NacosDbType != "mysql" {
		c.Run("echo '提醒：达梦库需先建 NACOS schema 并执行建表 SQL（离线包 middleware/common/nacos/sql/nacos-dm-2.5.1.sql），未建表时 Nacos 会启动失败或功能异常'")
	}
	return nil
}

// Cleanup 停服务、删 unit/目录。
func (n *Nacos) Cleanup(c *sshx.Client, p Params) error {
	return c.Run(fmt.Sprintf(`systemctl stop nacos 2>/dev/null; systemctl disable nacos 2>/dev/null; rm -f /etc/systemd/system/nacos.service; systemctl daemon-reload; rm -rf %s/nacos; echo '[清理] Nacos 痕迹已删除'`, p.InstallBase))
}
