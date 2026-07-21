package installer

import (
	"fmt"
	"path"
	"path/filepath"

	sshx "aksu-installer/internal/ssh"
)

// JDK 安装器：支持多版本共存。8 装到 /data/apps/jdk8、17 装到 /data/apps/jdk17，
// 软链 /data/apps/jdk 指向默认版本（手册里各中间件 restart.sh 写死
// /data/apps/jdk/bin/java，此软链必须保留）。环境变量 JAVA_HOME 也指向默认版本。
// 对应手册第 2 节，但把 wget 换成上传离线包。
type JDK struct{}

func (j *JDK) Name() string { return "JDK" }

// 离线包文件名（离线包/jdk/<arch>/ 下）。
var jdkFiles = map[string]map[string]string{
	"8": {
		"x86_64": "jdk-8u471-linux-x64.tar.gz",
		"arm64":  "jdk-8u471-linux-aarch64.tar.gz",
	},
	"17": {
		"x86_64": "jdk-17.0.17_linux-x64_bin.tar.gz",
		"arm64":  "jdk-17.0.16_linux-aarch64_bin.tar.gz",
	},
}

func (j *JDK) Install(c *sshx.Client, p Params, offlineRoot string) error {
	arch, err := DetectArch(c)
	if err != nil {
		return err
	}
	if len(p.JdkVersions) == 0 {
		return fmt.Errorf("未选择任何 JDK 版本")
	}
	// 默认版本兜底：若未指定或不在所选列表里，取第一个。
	def := p.JdkDefault
	if def == "" || !contains(p.JdkVersions, def) {
		def = p.JdkVersions[0]
	}

	if err := c.Run("mkdir -p " + p.TmpDir); err != nil {
		return err
	}

	for _, ver := range p.JdkVersions {
		files, ok := jdkFiles[ver]
		if !ok {
			return fmt.Errorf("不支持的 JDK 版本 %q（可选 8 或 17）", ver)
		}
		fileName := files[arch]
		localPath := filepath.Join(offlineRoot, "jdk", arch, fileName)
		remoteTar := path.Join(p.TmpDir, fileName)
		jdkHome := path.Join(p.InstallBase, "jdk"+ver) // /data/apps/jdk8 或 jdk17

		c.Run(fmt.Sprintf("echo '--- 安装 JDK %s -> %s ---'", ver, jdkHome))
		if err := c.Upload(localPath, remoteTar); err != nil {
			return err
		}

		// 解压到独立临时目录，取顶层目录名
		extractDir := path.Join(p.TmpDir, "jdk_extract_"+ver)
		if err := c.Run(fmt.Sprintf("rm -rf %s && mkdir -p %s && tar -xf %s -C %s", extractDir, extractDir, remoteTar, extractDir)); err != nil {
			return err
		}
		top, err := c.Output(fmt.Sprintf("ls -1 %s | head -1", extractDir))
		if err != nil || top == "" {
			return fmt.Errorf("JDK %s 解压后未找到目录: %v", ver, err)
		}
		// 移到版本化目录（幂等：先删旧）
		if err := c.Run(fmt.Sprintf("rm -rf %s && mv %s %s", jdkHome, path.Join(extractDir, top), jdkHome)); err != nil {
			return err
		}
		_ = c.Run(fmt.Sprintf("rm -rf %s %s", remoteTar, extractDir))
		if err := c.Run(jdkHome + "/bin/java -version"); err != nil {
			return fmt.Errorf("JDK %s 验证失败: %w", ver, err)
		}
	}

	// 软链 /data/apps/jdk 指向默认版本
	defHome := path.Join(p.InstallBase, "jdk"+def)
	jdkLink := path.Join(p.InstallBase, "jdk")
	if err := c.Run(fmt.Sprintf("ln -sfn %s %s", defHome, jdkLink)); err != nil {
		return err
	}

	// 环境变量 JAVA_HOME 指向软链（=默认版本）。$ 写字面量，交给远端 shell。
	profile := fmt.Sprintf(`export JAVA_HOME=%s\nexport PATH=$JAVA_HOME/bin:$PATH\n`, jdkLink)
	if err := c.Run(fmt.Sprintf("printf '%s' > /etc/profile.d/jdk.sh && chmod 644 /etc/profile.d/jdk.sh", profile)); err != nil {
		return err
	}

	c.Run(fmt.Sprintf("echo 'JDK 安装完成：已装版本 %v，默认 JDK %s（/data/apps/jdk -> jdk%s，JAVA_HOME 指向它）'", p.JdkVersions, def, def))
	c.Run("echo '其它版本可用绝对路径调用，如 " + path.Join(p.InstallBase, "jdk17") + "/bin/java'")
	return nil
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// Cleanup 删除 JDK 目录、软链、环境变量文件。
func (j *JDK) Cleanup(c *sshx.Client, p Params) error {
	base := p.InstallBase
	return c.Run(fmt.Sprintf("rm -rf %s/jdk %s/jdk8 %s/jdk17 /etc/profile.d/jdk.sh && echo '[清理] JDK 痕迹已删除'", base, base, base))
}
