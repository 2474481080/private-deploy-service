package installer

import (
	"fmt"

	sshx "aksu-installer/internal/ssh"
)

// FxToolApi 安装器：fx-python-tool-api（Django 应用，端口 30111）。
// 无网络现场无法 pip 安装依赖，故用预先 build 好的完整镜像 docker load + run。
type FxToolApi struct{}

func (f *FxToolApi) Name() string { return "fx-python-tool-api" }

const (
	fxImage = "fx-python-tool-api:latest"
	fxTar   = "fx-python-tool-api-latest.tar"
)

func (f *FxToolApi) Install(c *sshx.Client, p Params, offlineRoot string) error {
	if err := requireDocker(c); err != nil {
		return err
	}
	if err := loadImage(c, p, fxTar, fxImage); err != nil {
		return err
	}
	run := fmt.Sprintf(`docker rm -f fx-python-tool-api >/dev/null 2>&1
docker run -d --name fx-python-tool-api --restart=always -p 30111:30111 %s`, fxImage)
	if err := c.Run(run); err != nil {
		return fmt.Errorf("fx-python-tool-api 启动失败: %w", err)
	}
	check := `for i in $(seq 1 20); do code=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:30111/ 2>/dev/null); [ "$code" != "000" ] && echo "fx-python-tool-api 就绪 (HTTP $code)" && exit 0; sleep 2; done; echo 就绪超时; docker logs --tail 15 fx-python-tool-api; exit 1`
	if err := c.Run(check); err != nil {
		return fmt.Errorf("fx-python-tool-api 检查失败: %w", err)
	}
	c.Run("echo 'fx-python-tool-api 安装完成：http://本机IP:30111'")
	return nil
}

func (f *FxToolApi) Cleanup(c *sshx.Client, p Params) error {
	return c.Run(fmt.Sprintf(`docker rm -f fx-python-tool-api >/dev/null 2>&1; docker rmi %s >/dev/null 2>&1; echo '[清理] fx-python-tool-api 已删除'`, fxImage))
}
