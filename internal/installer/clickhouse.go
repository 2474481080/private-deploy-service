package installer

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	sshx "aksu-installer/internal/ssh"
)

// ClickHouse 安装器：上传 3 个 tgz -> 各自 doinst.sh 安装 -> config.d/users.d
// 覆盖数据路径/监听/密码 -> systemd 启动 -> 验证。对应手册第 13 节（tgz 版）。
type ClickHouse struct{}

func (ch *ClickHouse) Name() string { return "ClickHouse" }

// 安装顺序：common-static 必须最先。
var chPkgs = []string{"clickhouse-common-static", "clickhouse-server", "clickhouse-client"}

func (ch *ClickHouse) Install(c *sshx.Client, p Params, offlineRoot string) error {
	arch, err := DetectArch(c)
	if err != nil {
		return err
	}
	archTag := "amd64"
	if arch == "arm64" {
		archTag = "arm64"
	}
	dir := filepath.Join(offlineRoot, "database", arch, "clickhouse")
	dataDir := path.Join(p.InstallBase, "clickhouse")

	if err := c.Run("mkdir -p " + p.TmpDir + "/ch_extract"); err != nil {
		return err
	}
	_ = c.Run("systemctl stop clickhouse-server 2>/dev/null || true")

	// 1. 逐个上传 + 解压 + doinst.sh（非交互，default 先无密码，随后 users.d 配）
	for _, pkg := range chPkgs {
		matches, _ := filepath.Glob(filepath.Join(dir, pkg+"-*-"+archTag+".tgz"))
		if len(matches) == 0 {
			return fmt.Errorf("未找到 %s 的 %s 离线包（%s）", pkg, archTag, dir)
		}
		local := matches[0]
		remote := path.Join(p.TmpDir, filepath.Base(local))
		if err := c.Upload(local, remote); err != nil {
			return err
		}
		// 解压后目录名 = 包名-版本；执行其中 install/doinst.sh
		install := fmt.Sprintf(`cd %s/ch_extract && tar -xf %s && d=$(tar -tf %s | head -1 | cut -d/ -f1) && if [ -x "$d/install/doinst.sh" ]; then (cd "$d" && ./install/doinst.sh </dev/null >/tmp/ch_%s.log 2>&1) || (tail -15 /tmp/ch_%s.log; exit 1); fi`,
			p.TmpDir, remote, remote, pkg, pkg)
		if err := c.Run(install); err != nil {
			return fmt.Errorf("%s 安装失败: %w", pkg, err)
		}
		_ = c.Run("rm -f " + remote)
	}

	// 2. config.d 覆盖：监听所有地址 + 数据/临时路径到 /data/apps/clickhouse
	configXML := strings.Join([]string{
		"<clickhouse>",
		"  <listen_host>::</listen_host>",
		"  <path>" + dataDir + "/data/</path>",
		"  <tmp_path>" + dataDir + "/tmp/</tmp_path>",
		fmt.Sprintf("  <http_port>%s</http_port>", p.ChHttpPort),
		fmt.Sprintf("  <tcp_port>%s</tcp_port>", p.ChTcpPort),
		"</clickhouse>",
	}, "\\n")
	if err := c.Run(fmt.Sprintf("mkdir -p %s/data %s/tmp /etc/clickhouse-server/config.d /etc/clickhouse-server/users.d && printf '%s\\n' > /etc/clickhouse-server/config.d/aksu.xml", dataDir, dataDir, configXML)); err != nil {
		return err
	}

	// 3. users.d 覆盖：default 密码 + max_partitions
	usersXML := strings.Join([]string{
		"<clickhouse>",
		"  <users><default><password>" + p.ChPass + "</password></default></users>",
		"  <profiles><default><max_partitions_per_insert_block>100000</max_partitions_per_insert_block></default></profiles>",
		"</clickhouse>",
	}, "\\n")
	if err := c.Run(fmt.Sprintf("printf '%s\\n' > /etc/clickhouse-server/users.d/aksu.xml", usersXML)); err != nil {
		return err
	}
	// 建 clickhouse 用户（tgz 版 doinst.sh 不建，但 systemd unit 用 User=clickhouse）
	_ = c.Run("getent group clickhouse >/dev/null || groupadd -r clickhouse; id clickhouse >/dev/null 2>&1 || useradd -r -g clickhouse -s /sbin/nologin clickhouse 2>/dev/null || useradd -r -g clickhouse -s /usr/sbin/nologin clickhouse")
	// 建齐日志/数据目录并授权（clickhouse 用户对 /var/log、/var/lib 无 w，必须先建后 chown）
	_ = c.Run("mkdir -p /var/log/clickhouse-server /var/lib/clickhouse " + dataDir + "/data " + dataDir + "/tmp")
	_ = c.Run("chown -R clickhouse:clickhouse " + dataDir + " /etc/clickhouse-server /var/log/clickhouse-server /var/lib/clickhouse 2>/dev/null; rm -rf " + p.TmpDir + "/ch_extract")

	// 4. 用自己的 systemd unit（doinst 装的 unit 有 RuntimeDirectory 问题，会 233 失败）
	unit := strings.Join([]string{
		"[Unit]",
		"Description=ClickHouse Server",
		"After=network.target",
		"",
		"[Service]",
		"Type=simple",
		"User=clickhouse",
		"Group=clickhouse",
		"ExecStart=/usr/bin/clickhouse-server --config-file=/etc/clickhouse-server/config.xml",
		"Restart=on-failure",
		"RestartSec=5",
		"LimitNOFILE=500000",
		"",
		"[Install]",
		"WantedBy=multi-user.target",
	}, "\\n")
	if err := c.Run("rm -f /etc/systemd/system/clickhouse-server.service; printf '" + unit + "\\n' > /etc/systemd/system/clickhouse-server.service"); err != nil {
		return err
	}
	if err := c.Run("systemctl daemon-reload && systemctl enable clickhouse-server >/dev/null 2>&1; systemctl restart clickhouse-server"); err != nil {
		return fmt.Errorf("ClickHouse 启动失败: %w", err)
	}
	check := fmt.Sprintf(`for i in $(seq 1 20); do
  if echo 'SELECT 1' | curl -s "http://default:%s@127.0.0.1:%s/" --data-binary @- 2>/dev/null | grep -q 1; then echo "ClickHouse 验证通过 (SELECT 1)"; exit 0; fi
  sleep 2
done; echo 验证超时; journalctl -u clickhouse-server -n 15 --no-pager; exit 1`, p.ChPass, p.ChHttpPort)
	if err := c.Run(check); err != nil {
		return fmt.Errorf("ClickHouse 验证失败: %w", err)
	}
	c.Run(fmt.Sprintf("echo 'ClickHouse 安装完成：HTTP %s / TCP %s，数据 %s/data，账号 default'", p.ChHttpPort, p.ChTcpPort, dataDir))
	return nil
}

func (ch *ClickHouse) Cleanup(c *sshx.Client, p Params) error {
	return c.Run(fmt.Sprintf(`systemctl stop clickhouse-server 2>/dev/null; systemctl disable clickhouse-server 2>/dev/null; rm -f /etc/systemd/system/clickhouse-server.service /usr/lib/systemd/system/clickhouse-server.service; rm -rf /etc/clickhouse-server /etc/clickhouse-client /var/lib/clickhouse /var/log/clickhouse-server %s/clickhouse; rm -f /usr/bin/clickhouse*; systemctl daemon-reload; echo '[清理] ClickHouse 已删除'`, p.InstallBase))
}
