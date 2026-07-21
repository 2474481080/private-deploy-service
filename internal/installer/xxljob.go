package installer

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	sshx "aksu-installer/internal/ssh"
)

// XxlJob 安装器：上传 admin jar -> 外置 config/application.properties
// （数据库支持 达梦/MySQL 两种，连接串按类型生成）-> systemd -> HTTP 检查。
// 对应手册第 7 节。Spring Boot 会优先读工作目录 config/ 下的外置配置，
// 不用改 jar 包内文件。建表 SQL（离线包 xxl-job/sql/ 下按库类型选）需先在数据库执行。
type XxlJob struct{}

func (x *XxlJob) Name() string { return "XXL-JOB" }

const xxlJar = "xxl-job-admin-2.2.0.jar"

func (x *XxlJob) Install(c *sshx.Client, p Params, offlineRoot string) error {
	home := path.Join(p.InstallBase, "xxl-job")
	jdkLink := path.Join(p.InstallBase, "jdk")
	if _, err := c.Output("test -x " + jdkLink + "/bin/java && echo ok"); err != nil {
		return fmt.Errorf("未找到 %s/bin/java，请先安装 JDK", jdkLink)
	}
	if strings.TrimSpace(p.XxlDbHost) == "" {
		return fmt.Errorf("XXL-JOB 必须配置数据库地址（达梦或 MySQL）")
	}

	// 数据库连接串按类型生成
	var dbURL, driver string
	if p.XxlDbType == "mysql" {
		dbURL = fmt.Sprintf("jdbc:mysql://%s:%s/%s?useUnicode=true&characterEncoding=UTF-8&autoReconnect=true&serverTimezone=Asia/Shanghai&useSSL=false",
			p.XxlDbHost, p.XxlDbPort, p.XxlDbName)
		driver = "com.mysql.jdbc.Driver" // xxl-job 2.2.0 内置 mysql-connector 5.x
	} else {
		dbURL = fmt.Sprintf("jdbc:dm://%s:%s/%s?schema=%s&zeroDateTimeBehavior=convertToNull&useUnicode=true&characterEncoding=utf-8",
			p.XxlDbHost, p.XxlDbPort, p.XxlDbName, p.XxlDbName)
		driver = "dm.jdbc.driver.DmDriver"
	}

	if err := c.Run(fmt.Sprintf("mkdir -p %s/config %s/logs", home, home)); err != nil {
		return err
	}
	_ = c.Run("systemctl stop xxl-job 2>/dev/null || true")
	if err := c.Upload(filepath.Join(offlineRoot, "middleware", "common", "xxl-job", xxlJar), path.Join(home, xxlJar)); err != nil {
		return err
	}

	// 外置配置：覆盖 jar 内的端口与数据源
	conf := strings.Join([]string{
		"server.port=" + p.XxlPort,
		"spring.datasource.url=" + dbURL,
		"spring.datasource.username=" + p.XxlDbUser,
		"spring.datasource.password=" + p.XxlDbPass,
		"spring.datasource.driver-class-name=" + driver,
	}, "\\n")
	if err := c.Run(fmt.Sprintf("printf '%s\\n' > %s/config/application.properties && chmod 600 %s/config/application.properties", conf, home, home)); err != nil {
		return err
	}

	unit := strings.Join([]string{
		"[Unit]",
		"Description=XXL-JOB Admin",
		"After=network.target",
		"",
		"[Service]",
		"Type=simple",
		"WorkingDirectory=" + home, // Spring Boot 自动读 ./config/application.properties
		fmt.Sprintf("ExecStart=%s/bin/java -jar %s/%s", jdkLink, home, xxlJar),
		"Restart=on-failure",
		"RestartSec=10",
		"",
		"[Install]",
		"WantedBy=multi-user.target",
	}, "\\n")
	if err := c.Run(fmt.Sprintf("printf '%s\\n' > /etc/systemd/system/xxl-job.service", unit)); err != nil {
		return err
	}
	if err := c.Run("systemctl daemon-reload && systemctl enable xxl-job >/dev/null 2>&1; systemctl restart xxl-job"); err != nil {
		return fmt.Errorf("XXL-JOB 启动失败: %w", err)
	}

	// 端口就绪即算通过（登录路径因定制 context-path 而异，不写死）
	check := fmt.Sprintf(`for i in $(seq 1 30); do code=$(curl -s -o /dev/null -w '%%{http_code}' http://127.0.0.1:%s/ 2>/dev/null); [ "$code" != "000" ] && echo "XXL-JOB 检查通过 (HTTP $code)" && exit 0; sleep 3; done; echo 检查超时; journalctl -u xxl-job -n 20 --no-pager; exit 1`, p.XxlPort)
	if err := c.Run(check); err != nil {
		return fmt.Errorf("XXL-JOB 检查失败（常见原因：数据库未建表/连不上，先执行离线包 xxl-job/sql 下对应建表 SQL）: %w", err)
	}
	c.Run(fmt.Sprintf("echo 'XXL-JOB 安装完成：http://本机IP:%s/xxl-job-admin/toLogin（默认账号 admin，密码见交付记录）'", p.XxlPort))
	c.Run("echo '提醒：数据库需先执行建表 SQL（达梦: tables-xxl-job-dm.sql / MySQL: tables-xxl-job-mysql.sql）'")
	return nil
}

// Cleanup 停服务、删 unit/目录（数据库表不动）。
func (x *XxlJob) Cleanup(c *sshx.Client, p Params) error {
	return c.Run(fmt.Sprintf(`systemctl stop xxl-job 2>/dev/null; systemctl disable xxl-job 2>/dev/null; rm -f /etc/systemd/system/xxl-job.service; systemctl daemon-reload; rm -rf %s/xxl-job; echo '[清理] XXL-JOB 痕迹已删除（数据库表未动）'`, p.InstallBase))
}
