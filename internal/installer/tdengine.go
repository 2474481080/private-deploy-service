package installer

import (
	"fmt"
	"path"

	sshx "aksu-installer/internal/ssh"
)

// TDengine 安装器：docker load -> docker run（数据/日志卷）-> 检查。
// 对应手册第 14 节。
type TDengine struct{}

func (t *TDengine) Name() string { return "TDengine" }

const (
	tdImage = "tdengine/tsdb:3.3.8.8"
	tdTar   = "tdengine-3.3.8.8.tar"
)

func (t *TDengine) Install(c *sshx.Client, p Params, offlineRoot string) error {
	if err := requireDocker(c); err != nil {
		return err
	}
	if err := loadImage(c, p, tdTar, tdImage); err != nil {
		return err
	}
	base := path.Join(p.InstallBase, "tdengine")

	run := fmt.Sprintf(`mkdir -p %s/data %s/log
docker rm -f tdengine >/dev/null 2>&1
docker run -d --name tdengine --hostname db01 --privileged=true --restart=always \
  -p 6030:6030 -p 6041:6041 -p 6060:6060 \
  -v %s/data:/var/lib/taos -v %s/log:/var/log/taos \
  %s`, base, base, base, base, tdImage)
	if err := c.Run(run); err != nil {
		return fmt.Errorf("TDengine 启动失败: %w", err)
	}

	check := `for i in $(seq 1 20); do code=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:6041/ 2>/dev/null); [ "$code" != "000" ] && echo "TDengine 就绪 (REST HTTP $code)" && exit 0; sleep 3; done; echo 就绪超时; docker logs --tail 15 tdengine; exit 1`
	if err := c.Run(check); err != nil {
		return fmt.Errorf("TDengine 检查失败: %w", err)
	}
	c.Run("echo 'TDengine 安装完成：REST 6041 / 服务 6030 / Explorer http://本机IP:6060（默认 root/taosdata）'")
	return nil
}

func (t *TDengine) Cleanup(c *sshx.Client, p Params) error {
	return c.Run(fmt.Sprintf(`docker rm -f tdengine >/dev/null 2>&1; docker rmi %s >/dev/null 2>&1; rm -rf %s/tdengine; echo '[清理] TDengine 已删除'`, tdImage, p.InstallBase))
}
