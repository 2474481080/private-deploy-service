# 阿克苏私有化自动部署工具

Windows 上运行的本地 Web 程序：页面填写目标服务器和部署变量，后端 SSH 到目标机，
上传离线包并自动安装中间件，日志实时回传页面。

## 当前状态：v1.0（全 15 组件，改名"私有化部署服务"）

已实现组件（均在测试节点真机验证通过，幂等可重装）：
- **JDK**：8/17 多版本共存（jdk8/jdk17 目录 + /data/apps/jdk 软链指默认版本），应用服务器可同时装两个
- **Redis 6.2.9**：源码上传编译（依赖 yum/dnf/apt 自适应装 gcc/make），密码/端口变量，systemd Type=simple 托管，持久化按手册
- **MinIO**：按架构上传二进制（x86_64/arm64 都有包），端口/账号变量，systemd + 健康检查
- **Nacos 2.5.1**：数据库类型可选 达梦（自动放 dm 驱动+插件）/ MySQL（内置驱动），连接串/账号/schema/鉴权secret 全变量；不填库=内嵌 derby 测试模式；systemd + 就绪检查
- **Sentinel 1.8.2**：jar 上传，端口变量，systemd + HTTP 检查
- **XXL-JOB 2.2.0**：数据库类型可选 达梦/MySQL，外置 config/application.properties 覆盖 jar 配置（端口/连接串/账号全变量），systemd + 检查；登录路径 /xxl-job-admin/toLogin；两种库的建表 SQL 均在离线包 xxl-job/sql/
- **Nginx 1.26.1**：源码编译，端口变量，systemd（forking）+ 验证；软链 /usr/bin/nginx
- **ClickHouse 23.4.2.11**：tgz 离线安装（doinst.sh）+ 建 clickhouse 用户 + config.d/users.d 覆盖数据路径/监听/密码 + 自写 systemd（规避 doinst RuntimeDirectory 233）+ SELECT 1 验证
- **InfluxDB 2.7**：二进制解压 + systemd + HTTP setup API 初始化（用户/org/桶/token，幂等 201/422）
- **EMQX/RocketMQ官方/TDengine/Kafka/fx-python-tool-api**：docker load 本地镜像 + run（镜像在 F:\阿克苏镜像准备 按架构）
- **Docker 29.5.3**：静态二进制离线安装（x86_64/arm64 双架构包，不依赖 yum/apt 源），数据目录 /data/apps/docker，daemon.json 预留私有镜像仓库变量；机器上已有 docker 时自动跳过（绝不覆盖）

支持勾选多组件按依赖顺序批量安装（JDK 前置）。

## 用法

1. 双击 `aksu-installer.exe`，自动打开浏览器 http://127.0.0.1:8765
2. 填服务器 IP / 密码、确认安装目录和 JDK 版本，点“开始安装”
3. 右侧实时看安装日志

离线包目录默认 `E:\yjh\claude\aksu-deploy\离线包`，可用环境变量 `AKSU_OFFLINE_ROOT` 覆盖。

## 结构

- `main.go` — 本地 HTTP 服务、SSE 日志流、安装任务调度
- `internal/ssh/client.go` — SSH 执行（流式日志）+ SFTP 上传（带进度）
- `internal/installer/installer.go` — 安装器接口、架构探测、组件注册表
- `internal/installer/jdk.go` — JDK 安装器（打样组件）
- `web/index.html` — 内嵌前端页面

## 扩展新组件（后续）

按 `installer.Installer` 接口在 `internal/installer/` 加一个文件（参照 jdk.go），
在 `Registry()` 注册，前端加对应表单项即可。规划顺序：Nginx → 容器组件（EMQX/RocketMQ/TDengine，等用户的镜像仓库/镜像包就绪）。

离线包实际含：JDK(8/17, arm64+x86_64)、达梦、ClickHouse(x86)、InfluxDB(arm)、MinIO(arm64+x86_64)、Nacos、Sentinel、XXL-JOB、Redis 源码、Nginx 源码。

## 编译

```bash
GOPROXY=https://goproxy.cn,direct go build -o aksu-installer.exe .
```
