// Package sshx 封装到目标服务器的 SSH 连接、命令执行（流式回传日志）、
// 以及离线包上传（带进度）。安装器全部通过它操作远端。
package sshx

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Config 目标服务器连接参数。
type Config struct {
	Host     string
	Port     int
	User     string
	Password string // 密码认证；留空则用 KeyPath
	KeyPath  string // 私钥文件路径，可选
}

// LogFunc 接收一行日志（命令回显、进度等），由上层推给前端。
type LogFunc func(line string)

// Client 一次会话内复用的 SSH/SFTP 连接。
type Client struct {
	ssh  *ssh.Client
	sftp *sftp.Client
	log  LogFunc
}

func Dial(cfg Config, logf LogFunc) (*Client, error) {
	if logf == nil {
		logf = func(string) {}
	}
	auth := []ssh.AuthMethod{}
	if cfg.Password != "" {
		auth = append(auth, ssh.Password(cfg.Password))
	}
	if cfg.KeyPath != "" {
		key, err := os.ReadFile(cfg.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("读取私钥失败: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("解析私钥失败: %w", err)
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if len(auth) == 0 {
		return nil, fmt.Errorf("未提供密码或私钥")
	}
	port := cfg.Port
	if port == 0 {
		port = 22
	}
	conf := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // 内网批量部署，不校验 host key
		Timeout:         15 * time.Second,
	}
	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", port))
	logf(fmt.Sprintf("正在连接 %s ...", addr))
	sshClient, err := ssh.Dial("tcp", addr, conf)
	if err != nil {
		return nil, fmt.Errorf("SSH 连接失败: %w", err)
	}
	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		sshClient.Close()
		return nil, fmt.Errorf("SFTP 初始化失败: %w", err)
	}
	logf("连接成功。")
	return &Client{ssh: sshClient, sftp: sftpClient, log: logf}, nil
}

func (c *Client) Close() {
	if c.sftp != nil {
		c.sftp.Close()
	}
	if c.ssh != nil {
		c.ssh.Close()
	}
}

// Run 执行一条命令，stdout/stderr 逐行推给日志。返回退出码非 0 时报错。
func (c *Client) Run(cmd string) error {
	session, err := c.ssh.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	c.log("$ " + cmd)
	stdout, _ := session.StdoutPipe()
	stderr, _ := session.StderrPipe()
	if err := session.Start(cmd); err != nil {
		return err
	}
	done := make(chan struct{}, 2)
	pump := func(r io.Reader) {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 1024*1024), 1024*1024)
		for sc.Scan() {
			c.log(sc.Text())
		}
		done <- struct{}{}
	}
	go pump(stdout)
	go pump(stderr)
	<-done
	<-done
	if err := session.Wait(); err != nil {
		return fmt.Errorf("命令执行失败: %w", err)
	}
	return nil
}

// Output 执行命令并返回其标准输出（去尾换行），用于探测架构等。
func (c *Client) Output(cmd string) (string, error) {
	session, err := c.ssh.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	out, err := session.Output(cmd)
	return trimNewline(string(out)), err
}

// Upload 上传本地文件到远端，按 MB 粒度回报进度。
func (c *Client) Upload(localPath, remotePath string) error {
	src, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("打开本地文件失败: %w", err)
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return err
	}
	total := info.Size()

	if dir := path.Dir(remotePath); dir != "." && dir != "/" {
		_ = c.sftp.MkdirAll(dir)
	}
	dst, err := c.sftp.Create(remotePath)
	if err != nil {
		return fmt.Errorf("远端创建文件失败: %w", err)
	}
	defer dst.Close()

	c.log(fmt.Sprintf("上传 %s -> %s (%.1f MB)", path.Base(localPath), remotePath, float64(total)/1024/1024))
	buf := make([]byte, 512*1024)
	var written int64
	lastPct := -1
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return fmt.Errorf("写远端失败: %w", werr)
			}
			written += int64(n)
			if total > 0 {
				pct := int(written * 100 / total)
				if pct != lastPct && pct%10 == 0 {
					c.log(fmt.Sprintf("  上传进度 %d%%", pct))
					lastPct = pct
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("读本地失败: %w", rerr)
		}
	}
	c.log("上传完成。")
	return nil
}

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
