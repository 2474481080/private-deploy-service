package installer

import (
	"fmt"
	"path"

	sshx "aksu-installer/internal/ssh"
)

// EMQX 安装器：docker load 本地镜像 -> 首次从容器拷出 /opt/emqx 做持久化挂载
// -> docker run（端口映射 + 数据卷）-> 控制台就绪检查。对应手册第 10 节。
type EMQX struct{}

func (e *EMQX) Name() string { return "EMQX" }

const emqxImage = "emqx/emqx:4.4.19"
const emqxTar = "emqx-4.4.19.tar"

func (e *EMQX) Install(c *sshx.Client, p Params, offlineRoot string) error {
	if err := requireDocker(c); err != nil {
		return err
	}
	if err := loadImage(c, p, emqxTar, emqxImage); err != nil {
		return err
	}
	home := path.Join(p.InstallBase, "emqx")

	// 首次：临时容器拷出 /opt/emqx 到宿主机做持久化（手册 10.1）
	prep := fmt.Sprintf(`if [ ! -d %s ]; then
  docker rm -f emqx_tmp >/dev/null 2>&1
  docker run -d --name emqx_tmp %s >/dev/null && sleep 3
  docker cp emqx_tmp:/opt/emqx %s
  docker rm -f emqx_tmp >/dev/null 2>&1
fi`, home, emqxImage, home)
	if err := c.Run(prep); err != nil {
		return fmt.Errorf("初始化 EMQX 数据目录失败: %w", err)
	}

	run := fmt.Sprintf(`docker rm -f emqx >/dev/null 2>&1
docker run -d --name emqx --restart=always \
  -p 1883:1883 -p 8083:8083 -p 8084:8084 -p 8883:8883 -p 18083:18083 \
  -e EMQX_DASHBOARD__DEFAULT_USER__PASSWORD='%s' \
  -v %s:/opt/emqx %s`, p.EmqxDashPass, home, emqxImage)
	if err := c.Run(run); err != nil {
		return fmt.Errorf("EMQX 启动失败: %w", err)
	}

	check := `for i in $(seq 1 20); do code=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:18083/ 2>/dev/null); case "$code" in 200|302|301) echo "EMQX 控制台就绪 (HTTP $code)"; exit 0;; esac; sleep 3; done; echo 就绪超时; docker logs --tail 15 emqx; exit 1`
	if err := c.Run(check); err != nil {
		return fmt.Errorf("EMQX 就绪检查失败: %w", err)
	}
	c.Run(fmt.Sprintf("echo 'EMQX 安装完成：控制台 http://本机IP:18083（账号 admin，密码 %s）'", p.EmqxDashPass))
	return nil
}

func (e *EMQX) Cleanup(c *sshx.Client, p Params) error {
	return c.Run(fmt.Sprintf(`docker rm -f emqx emqx_tmp >/dev/null 2>&1; docker rmi %s >/dev/null 2>&1; rm -rf %s/emqx; echo '[清理] EMQX 已删除'`, emqxImage, p.InstallBase))
}
