// Package installer 定义组件安装器接口，以及各组件的具体实现。
// 打样阶段只实现 JDK，后续按同一接口扩展 Redis/Nacos 等。
package installer

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	sshx "aksu-installer/internal/ssh"
)

// osStat 便于容器安装器判断本地镜像文件是否存在。
func osStat(p string) (os.FileInfo, error) { return os.Stat(p) }

// Params 前端填写的部署变量。打样阶段字段较少，后续按组件扩充。
type Params struct {
	// 连接
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	KeyPath  string `json:"keyPath"`

	// 通用
	InstallBase string `json:"installBase"` // 默认 /data/apps
	TmpDir      string `json:"tmpDir"`      // 默认 /data/apps/tmp

	// JDK：可多选版本共存（应用服务器常需 8 和 17 同时装）
	JdkVersions []string `json:"jdkVersions"` // 如 ["8","17"]
	JdkDefault  string   `json:"jdkDefault"`  // 软链 /data/apps/jdk 和 JAVA_HOME 指向的版本

	// Redis
	RedisPort     string `json:"redisPort"`     // 默认 6379
	RedisPassword string `json:"redisPassword"` // 默认取手册值

	// MinIO
	MinioApiPort     string `json:"minioApiPort"`     // 默认 9000
	MinioConsolePort string `json:"minioConsolePort"` // 默认 39181
	MinioRootUser    string `json:"minioRootUser"`    // 默认 root
	MinioRootPass    string `json:"minioRootPass"`    // 默认取手册值

	// Nacos（DbHost 留空 = 内嵌 derby 测试模式）
	NacosPort        string `json:"nacosPort"`        // 默认 8848
	NacosDbType      string `json:"nacosDbType"`      // dameng | mysql，默认 dameng
	NacosDmHost      string `json:"nacosDmHost"`      // 数据库 IP，留空则内嵌库
	NacosDmPort      string `json:"nacosDmPort"`      // 默认 dameng=5236 / mysql=3306
	NacosDmSchema    string `json:"nacosDmSchema"`    // 默认 NACOS（mysql 为库名 nacos）
	NacosDmUser      string `json:"nacosDmUser"`      // 默认 dameng=SYSDBA / mysql=root
	NacosDmPass      string `json:"nacosDmPass"`      // 数据库密码
	NacosTokenSecret string `json:"nacosTokenSecret"` // 鉴权 token secret，默认手册值

	// Sentinel
	SentinelPort string `json:"sentinelPort"` // 默认 8849

	// XXL-JOB
	XxlPort   string `json:"xxlPort"`   // 默认 8081
	XxlDbType string `json:"xxlDbType"` // dameng | mysql，默认 dameng
	XxlDbHost string `json:"xxlDbHost"` // 数据库 IP（必填）
	XxlDbPort string `json:"xxlDbPort"` // 默认 dameng=5236 / mysql=3306
	XxlDbName string `json:"xxlDbName"` // 默认 dameng=XXL_JOB / mysql=xxl_job
	XxlDbUser string `json:"xxlDbUser"` // 默认 dameng=SYSDBA / mysql=root
	XxlDbPass string `json:"xxlDbPass"` // 数据库密码

	// Docker
	DockerMirror string `json:"dockerMirror"` // 私有镜像仓库/加速器地址，可选

	// 容器组件（EMQX/RocketMQ/TDengine/Kafka）：docker load 本地镜像 + run
	ImageRoot    string `json:"imageRoot"`    // 镜像根目录（默认 F:\阿克苏镜像准备）
	AdvertiseIP  string `json:"advertiseIP"`  // 对外发布 IP（Kafka/RocketMQ 用；留空自动探测目标机主 IP）
	EmqxDashPass string `json:"emqxDashPass"` // EMQX 控制台 admin 密码，默认取手册值

	// Nginx（源码编译）
	NginxPort string `json:"nginxPort"` // 默认 80

	// ClickHouse
	ChHttpPort string `json:"chHttpPort"` // 默认 8123
	ChTcpPort  string `json:"chTcpPort"`  // 默认 9000
	ChPass     string `json:"chPass"`     // default 用户密码，默认取手册值

	// InfluxDB
	InfluxPort   string `json:"influxPort"`   // 默认 8086
	InfluxUser   string `json:"influxUser"`   // 默认 admin
	InfluxPass   string `json:"influxPass"`   // 默认取手册值
	InfluxOrg    string `json:"influxOrg"`    // 默认 fx
	InfluxBucket string `json:"influxBucket"` // 默认 tian-ji-cloud
	InfluxToken  string `json:"influxToken"`  // 默认取手册私有化 token
}

// ApplyDefaults 填充未设置的字段为默认值。安装前调用。
func (p *Params) ApplyDefaults() {
	if p.Port == 0 {
		p.Port = 22
	}
	if p.User == "" {
		p.User = "root"
	}
	if p.InstallBase == "" {
		p.InstallBase = "/data/apps"
	}
	if p.TmpDir == "" {
		p.TmpDir = "/data/apps/tmp"
	}
	if len(p.JdkVersions) == 0 {
		p.JdkVersions = []string{"8"}
	}
	if p.JdkDefault == "" {
		p.JdkDefault = p.JdkVersions[0]
	}
	if p.RedisPort == "" {
		p.RedisPort = "6379"
	}
	if p.RedisPassword == "" {
		p.RedisPassword = "HaiOJPPAph21f2Cs" // 手册默认
	}
	if p.MinioApiPort == "" {
		p.MinioApiPort = "9000"
	}
	if p.MinioConsolePort == "" {
		p.MinioConsolePort = "39181"
	}
	if p.MinioRootUser == "" {
		p.MinioRootUser = "root"
	}
	if p.MinioRootPass == "" {
		p.MinioRootPass = "Cak9uNPYIPaWtZXu" // 手册默认
	}
	if p.EmqxDashPass == "" {
		p.EmqxDashPass = "public" // EMQX 默认，登录后应改
	}
	if p.NginxPort == "" {
		p.NginxPort = "80"
	}
	if p.ChHttpPort == "" {
		p.ChHttpPort = "8123"
	}
	if p.ChTcpPort == "" {
		p.ChTcpPort = "9000"
	}
	if p.ChPass == "" {
		p.ChPass = "Ft8wZXYrVUnLmaAe" // 手册默认
	}
	if p.InfluxPort == "" {
		p.InfluxPort = "8086"
	}
	if p.InfluxUser == "" {
		p.InfluxUser = "admin"
	}
	if p.InfluxPass == "" {
		p.InfluxPass = "qcExVVbmw63D2nwa" // 手册默认
	}
	if p.InfluxOrg == "" {
		p.InfluxOrg = "fx"
	}
	if p.InfluxBucket == "" {
		p.InfluxBucket = "tian-ji-cloud"
	}
	if p.InfluxToken == "" {
		p.InfluxToken = "RT8enTw9waSIUYgO7mZ7R8PQ2dhuy-l7tp5r7D3wI1lMbLjyMS9DGnQmItrpeOMur34R3yT99LO_wIcT8YN5-A=="
	}
	if p.NacosPort == "" {
		p.NacosPort = "8848"
	}
	if p.NacosDbType == "" {
		p.NacosDbType = "dameng"
	}
	if p.NacosDmPort == "" {
		p.NacosDmPort = map[string]string{"mysql": "3306"}[p.NacosDbType]
		if p.NacosDmPort == "" {
			p.NacosDmPort = "5236"
		}
	}
	if p.NacosDmSchema == "" {
		if p.NacosDbType == "mysql" {
			p.NacosDmSchema = "nacos"
		} else {
			p.NacosDmSchema = "NACOS"
		}
	}
	if p.NacosDmUser == "" {
		if p.NacosDbType == "mysql" {
			p.NacosDmUser = "root"
		} else {
			p.NacosDmUser = "SYSDBA"
		}
	}
	if p.NacosDmPass == "" {
		p.NacosDmPass = "YK1hv8G2I471k2wL" // 手册默认
	}
	if p.NacosTokenSecret == "" {
		p.NacosTokenSecret = "eXQ4eTQ3c1VZcSVSdkQzQFlNdVhWeVk0ZmpHUlojWFEK" // 手册默认
	}
	if p.SentinelPort == "" {
		p.SentinelPort = "8849"
	}
	if p.XxlPort == "" {
		p.XxlPort = "8081"
	}
	if p.XxlDbType == "" {
		p.XxlDbType = "dameng"
	}
	if p.XxlDbPort == "" {
		if p.XxlDbType == "mysql" {
			p.XxlDbPort = "3306"
		} else {
			p.XxlDbPort = "5236"
		}
	}
	if p.XxlDbName == "" {
		if p.XxlDbType == "mysql" {
			p.XxlDbName = "xxl_job"
		} else {
			p.XxlDbName = "XXL_JOB"
		}
	}
	if p.XxlDbUser == "" {
		if p.XxlDbType == "mysql" {
			p.XxlDbUser = "root"
		} else {
			p.XxlDbUser = "SYSDBA"
		}
	}
}

// Installer 一个可安装组件。
type Installer interface {
	Name() string
	// Install 在已连接的 client 上执行安装；offlineRoot 是本地离线包根目录。
	Install(c *sshx.Client, p Params, offlineRoot string) error
	// Cleanup 清理该组件的安装痕迹（服务、目录、unit、用户），幂等，
	// 用于"中断并清理"。只清理本工具会创建的东西。
	Cleanup(c *sshx.Client, p Params) error
}

// DetectOS 记录目标机系统与内核信息，返回内核主版本号（无法解析时 0）。
func DetectOS(c *sshx.Client) int {
	osName, _ := c.Output(`grep -E '^PRETTY_NAME=' /etc/os-release 2>/dev/null | cut -d'"' -f2`)
	kernel, _ := c.Output("uname -r")
	arch, _ := c.Output("uname -m")
	_ = c.Run(fmt.Sprintf("echo '目标系统：%s | 内核 %s | 架构 %s'", osName, kernel, arch))
	major := 0
	if i := strings.IndexByte(kernel, '.'); i > 0 {
		if v, err := strconv.Atoi(kernel[:i]); err == nil {
			major = v
		}
	}
	return major
}

// DetectArch 返回远端架构目录名：arm64 | x86_64。
func DetectArch(c *sshx.Client) (string, error) {
	out, err := c.Output("uname -m")
	if err != nil {
		return "", fmt.Errorf("探测架构失败: %w", err)
	}
	switch out {
	case "aarch64", "arm64":
		return "arm64", nil
	case "x86_64", "amd64":
		return "x86_64", nil
	default:
		return "", fmt.Errorf("未知架构 %q，离线包只提供 arm64/x86_64", out)
	}
}

// Registry 所有已实现的安装器，按 key 索引，供前端选择。
func Registry() map[string]Installer {
	return map[string]Installer{
		"jdk":        &JDK{},
		"redis":      &Redis{},
		"minio":      &MinIO{},
		"nacos":      &Nacos{},
		"sentinel":   &Sentinel{},
		"xxljob":     &XxlJob{},
		"docker":     &Docker{},
		"emqx":       &EMQX{},
		"rocketmq":   &RocketMQ{},
		"tdengine":   &TDengine{},
		"kafka":      &Kafka{},
		"fx":         &FxToolApi{},
		"nginx":      &Nginx{},
		"clickhouse": &ClickHouse{},
		"influxdb":   &InfluxDB{},
	}
}

// InstallOrder 批量安装时的推荐顺序（JDK 前置，Docker 在容器组件之前）。
var InstallOrder = []string{"jdk", "docker", "nginx", "redis", "minio", "clickhouse", "influxdb", "nacos", "sentinel", "xxljob", "emqx", "rocketmq", "tdengine", "kafka", "fx"}

// imageArchDir 把 uname -m 映射到镜像根目录下的架构子目录名。
func imageArchDir(c *sshx.Client) (string, error) {
	arch, err := DetectArch(c)
	if err != nil {
		return "", err
	}
	switch arch {
	case "x86_64":
		return "x86_64_amd64", nil
	case "arm64":
		return "arm64_aarch64", nil
	default:
		return "", fmt.Errorf("未知架构 %s", arch)
	}
}

// loadImage 从镜像根目录按架构找 tar，上传到目标机并 docker load。
func loadImage(c *sshx.Client, p Params, tarName, imageRef string) error {
	// 已有该镜像则跳过 load，避免重复上传大文件
	if _, err := c.Output("docker image inspect " + imageRef + " >/dev/null 2>&1 && echo ok"); err == nil {
		c.Run("echo '镜像 " + imageRef + " 已存在，跳过导入'")
		return nil
	}
	archDir, err := imageArchDir(c)
	if err != nil {
		return err
	}
	local := filepath.Join(p.ImageRoot, archDir, tarName)
	if _, err := osStat(local); err != nil {
		return fmt.Errorf("镜像文件不存在: %s（把 %s 放到镜像目录 %s\\%s 下）", local, tarName, p.ImageRoot, archDir)
	}
	remote := path.Join(p.TmpDir, tarName)
	if err := c.Run("mkdir -p " + p.TmpDir); err != nil {
		return err
	}
	if err := c.Upload(local, remote); err != nil {
		return err
	}
	c.Run("echo '导入镜像 " + imageRef + " ...'")
	if err := c.Run("docker load -i " + remote); err != nil {
		return fmt.Errorf("docker load 失败: %w", err)
	}
	_ = c.Run("rm -f " + remote)
	return nil
}

// primaryIP 返回目标机主 IP（hostname -I 第一个），用于 Kafka/RocketMQ 对外发布地址。
func primaryIP(c *sshx.Client) string {
	out, _ := c.Output("hostname -I 2>/dev/null | awk '{print $1}'")
	return strings.TrimSpace(out)
}

// requireDocker 确认目标机 docker 可用（容器组件前置）。
func requireDocker(c *sshx.Client) error {
	if _, err := c.Output("command -v docker >/dev/null && docker info >/dev/null 2>&1 && echo ok"); err != nil {
		return fmt.Errorf("Docker 不可用，请先安装并启动 Docker（勾选 Docker 组件）")
	}
	return nil
}

// DetectPkgManager 返回远端包管理器命令前缀：yum/dnf/apt。
func DetectPkgManager(c *sshx.Client) (string, error) {
	for _, pm := range []string{"dnf", "yum", "apt-get"} {
		if _, err := c.Output("command -v " + pm); err == nil {
			return pm, nil
		}
	}
	return "", fmt.Errorf("未找到 yum/dnf/apt-get，无法安装编译依赖")
}
