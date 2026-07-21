package installer

import (
	"fmt"
	"path"
	"strings"

	sshx "aksu-installer/internal/ssh"
)

// Kafka 安装器：KRaft 单机模式（无需 ZooKeeper），host 网络。
// 对应 F:\阿克苏镜像准备 下的 Kafka 部署文档。ADVERTISED_LISTENERS 需对外 IP。
type Kafka struct{}

func (k *Kafka) Name() string { return "Kafka" }

const (
	kafkaImage = "apache/kafka:4.3.0"
	kafkaTar   = "kafka-4.3.0.tar"
)

func (k *Kafka) Install(c *sshx.Client, p Params, offlineRoot string) error {
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
	if err := loadImage(c, p, kafkaTar, kafkaImage); err != nil {
		return err
	}
	base := path.Join(p.InstallBase, "kafka")

	run := fmt.Sprintf(`mkdir -p %s/data %s/logs && chmod -R 777 %s
docker rm -f kafka >/dev/null 2>&1
docker run -d --name kafka --restart=always --network host \
  -e KAFKA_NODE_ID=1 \
  -e KAFKA_PROCESS_ROLES=broker,controller \
  -e KAFKA_LISTENERS=PLAINTEXT://:9092,CONTROLLER://:9093 \
  -e KAFKA_ADVERTISED_LISTENERS=PLAINTEXT://%s:9092 \
  -e KAFKA_CONTROLLER_LISTENER_NAMES=CONTROLLER \
  -e KAFKA_LISTENER_SECURITY_PROTOCOL_MAP=CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT \
  -e KAFKA_CONTROLLER_QUORUM_VOTERS=1@%s:9093 \
  -e KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR=1 \
  -e KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR=1 \
  -e KAFKA_TRANSACTION_STATE_LOG_MIN_ISR=1 \
  -e KAFKA_GROUP_INITIAL_REBALANCE_DELAY_MS=0 \
  -e KAFKA_LOG_DIRS=/var/lib/kafka/data \
  -v %s/data:/var/lib/kafka/data \
  %s`, base, base, base, ip, ip, base, kafkaImage)
	if err := c.Run(run); err != nil {
		return fmt.Errorf("Kafka 启动失败: %w", err)
	}

	// 端口就绪 + 建测试 topic 验证
	check := fmt.Sprintf(`for i in $(seq 1 20); do
  if ss -lnt 2>/dev/null | grep -q ':9092'; then
    if docker exec kafka /opt/kafka/bin/kafka-topics.sh --bootstrap-server %s:9092 --create --topic aksu-selftest --partitions 1 --replication-factor 1 >/dev/null 2>&1; then
      docker exec kafka /opt/kafka/bin/kafka-topics.sh --bootstrap-server %s:9092 --delete --topic aksu-selftest >/dev/null 2>&1
      echo "Kafka 就绪：建/删测试 topic 成功"; exit 0
    fi
  fi
  sleep 3
done
echo 就绪超时; docker logs --tail 20 kafka; exit 1`, ip, ip)
	if err := c.Run(check); err != nil {
		return fmt.Errorf("Kafka 检查失败: %w", err)
	}
	c.Run(fmt.Sprintf("echo 'Kafka 安装完成：broker %s:9092（KRaft 单机，controller 9093）'", ip))
	return nil
}

func (k *Kafka) Cleanup(c *sshx.Client, p Params) error {
	return c.Run(fmt.Sprintf(`docker rm -f kafka >/dev/null 2>&1; docker rmi %s >/dev/null 2>&1; rm -rf %s/kafka; echo '[清理] Kafka 已删除'`, kafkaImage, p.InstallBase))
}
