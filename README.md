# 私有化部署服务

Windows 上运行的本地 Web 程序：网页填写目标服务器和部署变量，后端 SSH 到目标机，
上传离线包/镜像并自动安装中间件，日志实时回传页面。**离线优先，x86_64 / arm64 双架构自动适配。**

## 用法

1. 双击 `私有化部署服务.exe`（或 `aksu-installer.exe`），浏览器自动打开 http://127.0.0.1:8765
2. 填目标服务器 IP / 密码（或用免密），勾选要装的组件，填变量（都预填了默认值）
3. 点开始安装，右侧实时看日志

程序默认从 exe 同级目录读 `离线包/` 和 `镜像/`（可用环境变量 `AKSU_OFFLINE_ROOT`、`AKSU_IMAGE_ROOT` 覆盖）。

## 支持的组件（15）

| 类型 | 组件 |
| --- | --- |
| 运行时 | JDK 8/17（多版本共存） |
| 容器引擎 | Docker（静态二进制，老内核自动降级 20.10） |
| 缓存/存储 | Redis、MinIO |
| 注册/配置/调度 | Nacos、XXL-JOB（均支持达梦 / MySQL 双库） |
| 限流 | Sentinel |
| Web | Nginx（源码编译） |
| 数据库 | ClickHouse、InfluxDB |
| 消息/时序（容器） | EMQX、RocketMQ（官方）、TDengine、Kafka |
| 应用（容器） | fx-python-tool-api |

## 特性

- **离线优先**：非容器组件用离线包上传安装，容器组件 `docker load` 本地镜像库，全程不依赖外网
- **双架构**：按目标机 `uname -m` 自动选 x86_64 / arm64 的包与镜像
- **双数据库**：Nacos / XXL-JOB 达梦、MySQL 一键切换，连接串/账号/建表 SQL 按库类型走
- **免密**：程序生成本机公钥 + 一键配置命令，多台共用，附一键删除命令
- **中断并清理**：全程中断按钮，中断后自动重连清理已装痕迹（防误触需输入确认字）
- **多系统适配**：麒麟 / openEuler / CentOS 7+ / Ubuntu；CentOS 7 老内核自动用 Docker 20.10 兼容版

## 分发包（现场使用）

源码在本仓库；离线包与镜像因体积大不入库，单独打包分发，**按架构各一份**：

| 包 | 内容 |
| --- | --- |
| `私有化部署服务_x86_64` / `_arm64` | exe + 离线包（对应架构）+ 文档 |
| `镜像包_x86_64` / `_arm64` | 容器组件镜像 tar（对应架构） |

现场按目标机架构，取对应的主程序包 + 镜像包，解压后把镜像目录放进主程序的 `镜像/` 下即可。

## 现场唯一需自备

Redis / Nginx 源码编译需目标机有 `gcc` + `pcre/zlib/openssl-devel`。纯无网络环境**挂系统安装 ISO 配本地 yum 源**即可离线安装——这是私有化交付的标准动作。详见 `无网络部署依赖清单.md`。

## 从源码编译

```bash
GOPROXY=https://goproxy.cn,direct go build -o aksu-installer.exe .
```

## 结构

- `main.go` — 本地 HTTP 服务、SSE 日志流、任务/中断/清理、免密公钥
- `internal/ssh/` — SSH 执行 + SFTP 上传（带进度）
- `internal/installer/` — 15 个组件安装器 + 通用框架（架构探测、镜像 load、编译工具链检测）
- `web/index.html` — 内嵌前端页面
