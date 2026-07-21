package installer

import (
	"fmt"
	"path"
	"path/filepath"

	sshx "aksu-installer/internal/ssh"
)

// InfluxDB 安装器：按架构选 tar.gz -> 解压到 /data/apps/influxdb -> systemd 起 influxd
// -> 就绪后 influx setup 初始化（用户/组织/桶/token）。对应手册第 12 节。
type InfluxDB struct{}

func (i *InfluxDB) Name() string { return "InfluxDB" }

func (i *InfluxDB) Install(c *sshx.Client, p Params, offlineRoot string) error {
	arch, err := DetectArch(c)
	if err != nil {
		return err
	}
	dir := filepath.Join(offlineRoot, "database", arch, "influxdb")
	matches, _ := filepath.Glob(filepath.Join(dir, "influxdb2-*.tar.gz"))
	if len(matches) == 0 {
		return fmt.Errorf("未找到 InfluxDB 离线包（%s 下无 influxdb2-*.tar.gz）", dir)
	}
	localPath := matches[0]
	fileName := filepath.Base(localPath)
	home := path.Join(p.InstallBase, "influxdb")
	dataDir := home + "/data"
	remoteTar := path.Join(p.TmpDir, fileName)

	// 1. 上传 + 解压，二进制移到 /data/apps/influxdb
	if err := c.Run(fmt.Sprintf("mkdir -p %s %s/influx_extract", p.TmpDir, p.TmpDir)); err != nil {
		return err
	}
	if err := c.Upload(localPath, remoteTar); err != nil {
		return err
	}
	_ = c.Run("systemctl stop influxdb 2>/dev/null || true")
	// 2.7 tar 里二进制在 usr/bin/influxd（无独立 influx CLI），只取 influxd
	unpack := fmt.Sprintf("rm -rf %s/influx_extract && mkdir -p %s/influx_extract %s && tar -xf %s -C %s/influx_extract && f=$(find %s/influx_extract -name influxd -type f | head -1) && cp -f $f %s/ && chmod +x %s/influxd && rm -rf %s/influx_extract",
		p.TmpDir, p.TmpDir, dataDir, remoteTar, p.TmpDir, p.TmpDir, home, home, p.TmpDir)
	if err := c.Run(unpack); err != nil {
		return fmt.Errorf("解压 InfluxDB 失败: %w", err)
	}

	// 2. systemd（数据目录指到 /data/apps/influxdb/data）
	unit := "[Unit]\\nDescription=InfluxDB 2\\nAfter=network.target\\n\\n[Service]\\n" +
		fmt.Sprintf("ExecStart=%s/influxd --bolt-path %s/influxd.bolt --engine-path %s/engine --http-bind-address :%s\\n", home, dataDir, dataDir, p.InfluxPort) +
		"Restart=on-failure\\nRestartSec=5\\nLimitNOFILE=65536\\n\\n[Install]\\nWantedBy=multi-user.target"
	if err := c.Run(fmt.Sprintf("printf '%s\\n' > /etc/systemd/system/influxdb.service", unit)); err != nil {
		return err
	}
	if err := c.Run("systemctl daemon-reload && systemctl enable influxdb >/dev/null 2>&1; systemctl restart influxdb"); err != nil {
		return fmt.Errorf("InfluxDB 启动失败: %w", err)
	}

	// 3. 就绪检查
	ready := fmt.Sprintf(`for i in $(seq 1 20); do curl -sf http://127.0.0.1:%s/health >/dev/null 2>&1 && echo "InfluxDB 就绪" && exit 0; sleep 2; done; echo 就绪超时; journalctl -u influxdb -n 15 --no-pager; exit 1`, p.InfluxPort)
	if err := c.Run(ready); err != nil {
		return fmt.Errorf("InfluxDB 就绪检查失败: %w", err)
	}

	// 4. 初始化（直接 POST setup：201=成功，422=已初始化跳过；不依赖 influx CLI，幂等）
	setup := fmt.Sprintf(`code=$(curl -s -o /dev/null -w '%%{http_code}' -X POST http://127.0.0.1:%s/api/v2/setup -H 'Content-Type: application/json' \
  -d '{"username":"%s","password":"%s","org":"%s","bucket":"%s","token":"%s","retentionPeriodSeconds":0}')
case "$code" in
  201) echo "InfluxDB 初始化完成" ;;
  422) echo "InfluxDB 已初始化，跳过 setup" ;;
  *) echo "初始化失败 HTTP $code"; exit 1 ;;
esac`, p.InfluxPort, p.InfluxUser, p.InfluxPass, p.InfluxOrg, p.InfluxBucket, p.InfluxToken)
	if err := c.Run(setup); err != nil {
		return fmt.Errorf("InfluxDB 初始化失败: %w", err)
	}

	_ = c.Run("rm -f " + remoteTar)
	c.Run(fmt.Sprintf("echo 'InfluxDB 安装完成：http://本机IP:%s（账号 %s，组织 %s，桶 %s）'", p.InfluxPort, p.InfluxUser, p.InfluxOrg, p.InfluxBucket))
	return nil
}

func (i *InfluxDB) Cleanup(c *sshx.Client, p Params) error {
	return c.Run(fmt.Sprintf(`systemctl stop influxdb 2>/dev/null; systemctl disable influxdb 2>/dev/null; rm -f /etc/systemd/system/influxdb.service; systemctl daemon-reload; rm -rf %s/influxdb; echo '[清理] InfluxDB 已删除'`, p.InstallBase))
}
