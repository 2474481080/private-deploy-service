package installer

import (
	"fmt"
	"path"
	"strings"

	sshx "aksu-installer/internal/ssh"
)

// RocketMQ 安装器：namesrv + broker + console 三容器（手册第 11 节）。
// broker 需要 broker.conf（brokerIP1=对外 IP），console 连 namesrv。
type RocketMQ struct{}

func (r *RocketMQ) Name() string { return "RocketMQ" }

const (
	rmqImage        = "apache/rocketmq:5.0.0"
	rmqConsoleImage = "apacherocketmq/rocketmq-dashboard:latest"
	rmqTar          = "rocketmq-5.0.0.tar"
	rmqConsoleTar   = "rocketmq-dashboard-latest.tar"
	rmqHome         = "/home/rocketmq/rocketmq-5.0.0" // 官方镜像内 RocketMQ 目录
)

func (r *RocketMQ) Install(c *sshx.Client, p Params, offlineRoot string) error {
	if err := requireDocker(c); err != nil {
		return err
	}
	ip := strings.TrimSpace(p.AdvertiseIP)
	if ip == "" {
		ip = primaryIP(c)
		if ip == "" {
			return fmt.Errorf("无法探测本机 IP，请在页面填写对外发布 IP")
		}
		c.Run("echo '对外 IP 自动探测为 " + ip + "'")
	}
	if err := loadImage(c, p, rmqTar, rmqImage); err != nil {
		return err
	}
	if err := loadImage(c, p, rmqConsoleTar, rmqConsoleImage); err != nil {
		return err
	}
	base := path.Join(p.InstallBase, "rocketmq")

	// broker.conf（手册 11.3）
	brokerConf := strings.Join([]string{
		"brokerClusterName = DefaultCluster",
		"brokerName = broker-a",
		"brokerId = 0",
		"deleteWhen = 04",
		"fileReservedTime = 48",
		"brokerRole = ASYNC_MASTER",
		"flushDiskType = ASYNC_FLUSH",
		"brokerIP1 = " + ip,
	}, "\\n")
	setup := fmt.Sprintf("mkdir -p %s/namesrv/logs %s/broker/conf %s/broker/store %s/broker/logs && printf '%s\\n' > %s/broker/conf/broker.conf",
		base, base, base, base, brokerConf, base)
	if err := c.Run(setup); err != nil {
		return err
	}

	// NameServer（手册 11.2）
	nsRun := fmt.Sprintf(`docker rm -f rmqnamesrv >/dev/null 2>&1
docker run -d --name rmqnamesrv -p 9876:9876 --restart=always --ulimit nofile=65535:65535 \
  -v %s/namesrv/logs:/home/rocketmq/logs \
  -e "JAVA_OPT_EXT=-Xms512m -Xmx512m -Xmn256m" \
  %s sh mqnamesrv`, base, rmqImage)
	if err := c.Run(nsRun); err != nil {
		return fmt.Errorf("NameServer 启动失败: %w", err)
	}

	// Broker（官方镜像 broker.conf 在 /home/rocketmq/rocketmq-5.0.0/conf 下）
	brokerRun := fmt.Sprintf(`docker rm -f rmqbroker >/dev/null 2>&1
docker run -d --name rmqbroker --link rmqnamesrv:namesrv -p 10911:10911 -p 10909:10909 --restart=always --ulimit nofile=65535:65535 \
  -v %s/broker/conf/broker.conf:%s/conf/broker.conf \
  -v %s/broker/logs:/home/rocketmq/logs \
  -v %s/broker/store:/home/rocketmq/store \
  -e "NAMESRV_ADDR=namesrv:9876" \
  -e "JAVA_OPT_EXT=-server -Xms1g -Xmx1g -Xmn512m" \
  %s sh mqbroker -c %s/conf/broker.conf`, base, rmqHome, base, base, rmqImage, rmqHome)
	if err := c.Run(brokerRun); err != nil {
		return fmt.Errorf("Broker 启动失败: %w", err)
	}

	// Dashboard（官方 rocketmq-dashboard，容器内 8080 映射到宿主 8088）
	consoleRun := fmt.Sprintf(`docker rm -f rmqconsole >/dev/null 2>&1
docker run -d --name rmqconsole --link rmqnamesrv:namesrv -p 8088:8080 --restart=always \
  -e "JAVA_OPTS=-Drocketmq.namesrv.addr=namesrv:9876 -Drocketmq.config.isVIPChannel=false" \
  %s`, rmqConsoleImage)
	if err := c.Run(consoleRun); err != nil {
		return fmt.Errorf("Dashboard 启动失败: %w", err)
	}

	check := `for i in $(seq 1 25); do code=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8088/ 2>/dev/null); case "$code" in 200|302) echo "RocketMQ 控制台就绪 (HTTP $code)"; exit 0;; esac; sleep 3; done; echo 就绪超时; docker ps -a | grep rmq; exit 1`
	if err := c.Run(check); err != nil {
		return fmt.Errorf("RocketMQ 检查失败: %w", err)
	}
	c.Run(fmt.Sprintf("echo 'RocketMQ 安装完成：控制台 http://本机IP:8088，NameServer %s:9876'", ip))
	return nil
}

func (r *RocketMQ) Cleanup(c *sshx.Client, p Params) error {
	return c.Run(fmt.Sprintf(`docker rm -f rmqnamesrv rmqbroker rmqconsole >/dev/null 2>&1; docker rmi %s %s >/dev/null 2>&1; rm -rf %s/rocketmq; echo '[清理] RocketMQ 已删除'`, rmqImage, rmqConsoleImage, p.InstallBase))
}
