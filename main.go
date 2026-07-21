// aksu-installer：本地 Web 程序。双击 exe 启动后浏览器打开 http://127.0.0.1:8765，
// 页面填写目标服务器和部署变量，后端 SSH 到目标机执行安装，日志经 SSE 实时回传。
// 支持：多组件批量安装、全程中断并清理安装痕迹、本机免密公钥生成。
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"embed"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"aksu-installer/internal/installer"
	sshx "aksu-installer/internal/ssh"
	gossh "golang.org/x/crypto/ssh"
)

//go:embed web/index.html
var webFS embed.FS

const listenAddr = "127.0.0.1:8765"

// 镜像根目录（容器组件从这里按架构读 docker save 的 tar），main() 里初始化。
var imageRoot string

// ---------------------------------------------------------------------------
// 日志广播

type logHub struct {
	mu      sync.Mutex
	subs    map[chan string]struct{}
	history []string
	done    bool
}

func newLogHub() *logHub { return &logHub{subs: map[chan string]struct{}{}} }

func (h *logHub) publish(line string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.done {
		return
	}
	h.history = append(h.history, line)
	for ch := range h.subs {
		select {
		case ch <- line:
		default:
		}
	}
}

func (h *logHub) finish() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.done {
		return
	}
	h.done = true
	for ch := range h.subs {
		close(ch)
		delete(h.subs, ch)
	}
}

func (h *logHub) subscribe() (chan string, []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan string, 256)
	hist := append([]string{}, h.history...)
	if !h.done {
		h.subs[ch] = struct{}{}
	} else {
		close(ch)
	}
	return ch, hist
}

// ---------------------------------------------------------------------------
// 任务：一次批量安装。支持中断（关 SSH 连接）+ 清理痕迹。

type task struct {
	mu       sync.Mutex
	hub      *logHub
	client   *sshx.Client
	insts    []installer.Installer
	params   installer.Params
	aborting bool
	running  bool
}

var (
	curTask   *task
	curTaskMu sync.Mutex
)

func (t *task) setClient(c *sshx.Client) {
	t.mu.Lock()
	t.client = c
	t.mu.Unlock()
}

func (t *task) isAborting() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.aborting
}

func main() {
	// 默认从 exe 同级目录读离线包与镜像（打包解压后双击即用，无需设环境变量）
	base := exeDir()
	offlineRoot := os.Getenv("AKSU_OFFLINE_ROOT")
	if offlineRoot == "" {
		offlineRoot = filepath.Join(base, "离线包")
	}
	imageRoot = os.Getenv("AKSU_IMAGE_ROOT")
	if imageRoot == "" {
		imageRoot = filepath.Join(base, "镜像")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		data, _ := webFS.ReadFile("web/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	})

	// 触发安装
	mux.HandleFunc("/api/install", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		var body struct {
			Components []string         `json:"components"`
			Params     installer.Params `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if len(body.Components) == 0 {
			writeJSON(w, 400, map[string]string{"error": "未选择任何组件"})
			return
		}
		curTaskMu.Lock()
		if curTask != nil && curTask.running {
			curTaskMu.Unlock()
			writeJSON(w, 409, map[string]string{"error": "已有安装任务在进行中"})
			return
		}
		curTaskMu.Unlock()

		reg := installer.Registry()
		selected := map[string]bool{}
		for _, k := range body.Components {
			if _, ok := reg[k]; !ok {
				writeJSON(w, 400, map[string]string{"error": "未知组件: " + k})
				return
			}
			selected[k] = true
		}
		var insts []installer.Installer
		for _, k := range installer.InstallOrder {
			if selected[k] {
				insts = append(insts, reg[k])
			}
		}

		t := &task{hub: newLogHub(), insts: insts, params: body.Params, running: true}
		curTaskMu.Lock()
		curTask = t
		curTaskMu.Unlock()

		go runInstall(t, offlineRoot)
		writeJSON(w, 200, map[string]string{"status": "started"})
	})

	// 中断并清理：关闭连接打断当前命令，重连后按注册顺序清理全部所选组件痕迹
	mux.HandleFunc("/api/abort", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		curTaskMu.Lock()
		t := curTask
		curTaskMu.Unlock()
		if t == nil {
			writeJSON(w, 404, map[string]string{"error": "没有任务"})
			return
		}
		t.mu.Lock()
		alreadyAborting := t.aborting
		t.aborting = true
		client := t.client
		running := t.running
		t.mu.Unlock()
		if alreadyAborting {
			writeJSON(w, 200, map[string]string{"status": "aborting"})
			return
		}
		t.hub.publish("!!! 收到中断指令，正在停止并清理安装痕迹 ...")
		if client != nil && running {
			client.Close() // 打断正在执行的远端命令
		}
		go cleanupTask(t)
		writeJSON(w, 200, map[string]string{"status": "aborting"})
	})

	// 失败后手动清理（复用中断清理逻辑）
	mux.HandleFunc("/api/cleanup", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		curTaskMu.Lock()
		t := curTask
		curTaskMu.Unlock()
		if t == nil {
			writeJSON(w, 404, map[string]string{"error": "没有任务"})
			return
		}
		t.mu.Lock()
		if t.running {
			t.mu.Unlock()
			writeJSON(w, 409, map[string]string{"error": "任务进行中，请用中断按钮"})
			return
		}
		t.aborting = true
		t.mu.Unlock()
		// 失败后 hub 已 finish，换新 hub 输出清理日志
		t.hub = newLogHub()
		go cleanupTask(t)
		writeJSON(w, 200, map[string]string{"status": "cleaning"})
	})

	// SSE 日志流
	mux.HandleFunc("/api/logs", func(w http.ResponseWriter, r *http.Request) {
		curTaskMu.Lock()
		var hub *logHub
		if curTask != nil {
			hub = curTask.hub
		}
		curTaskMu.Unlock()
		if hub == nil {
			http.Error(w, "no task", 404)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, _ := w.(http.Flusher)
		ch, hist := hub.subscribe()
		for _, line := range hist {
			fmt.Fprintf(w, "data: %s\n\n", jsonLine(line))
		}
		if flusher != nil {
			flusher.Flush()
		}
		for line := range ch {
			fmt.Fprintf(w, "data: %s\n\n", jsonLine(line))
			if flusher != nil {
				flusher.Flush()
			}
		}
		fmt.Fprintf(w, "event: done\ndata: end\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	})

	// 免密：生成/读取本机 SSH 密钥对，返回公钥与配置/删除命令
	mux.HandleFunc("/api/pubkey", func(w http.ResponseWriter, r *http.Request) {
		pub, err := ensureKeyPair()
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		pub = strings.TrimSpace(pub)
		writeJSON(w, 200, map[string]string{
			"pubkey":     pub,
			"installCmd": fmt.Sprintf("mkdir -p ~/.ssh && chmod 700 ~/.ssh && echo '%s' >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys", pub),
			"removeCmd":  "sed -i '/aksu-installer/d' ~/.ssh/authorized_keys",
		})
	})

	log.Printf("aksu-installer 启动：http://%s  （离线包目录: %s）", listenAddr, offlineRoot)
	go openBrowser("http://" + listenAddr)
	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		log.Fatal(err)
	}
}

func runInstall(t *task, offlineRoot string) {
	logf := func(line string) { t.hub.publish(line) }
	finish := func() {
		t.mu.Lock()
		t.running = false
		aborting := t.aborting
		t.mu.Unlock()
		if !aborting { // 中断时由清理协程负责收尾
			t.hub.finish()
		}
	}
	defer finish()

	p := &t.params
	p.ApplyDefaults()
	if p.ImageRoot == "" {
		p.ImageRoot = imageRoot
	}
	// 密码留空时自动用本机免密私钥
	if p.Password == "" && p.KeyPath == "" {
		if kp := keyPath(); fileExists(kp) {
			p.KeyPath = kp
			logf("未填密码，使用本机免密私钥连接（请确认已在目标机配置公钥）")
		}
	}

	names := make([]string, len(t.insts))
	for i, inst := range t.insts {
		names[i] = inst.Name()
	}
	logf(fmt.Sprintf("=== 部署任务开始：%s ===", strings.Join(names, " -> ")))
	client, err := sshx.Dial(sshx.Config{
		Host: p.Host, Port: p.Port, User: p.User,
		Password: p.Password, KeyPath: p.KeyPath,
	}, logf)
	if err != nil {
		logf("错误: " + err.Error())
		logf("=== 安装失败 ===")
		return
	}
	t.setClient(client)
	defer client.Close()

	installer.DetectOS(client)

	for _, inst := range t.insts {
		if t.isAborting() {
			return
		}
		start := time.Now()
		logf(fmt.Sprintf("=== [%s] 开始安装 ===", inst.Name()))
		if err := inst.Install(client, *p, offlineRoot); err != nil {
			if t.isAborting() {
				return // 中断导致的报错不再提示
			}
			logf("错误: " + err.Error())
			logf(fmt.Sprintf("=== [%s] 安装失败，任务中止（可点\"清理安装痕迹\"回滚本次改动） ===", inst.Name()))
			return
		}
		logf(fmt.Sprintf("=== [%s] 安装成功，用时 %s ===", inst.Name(), time.Since(start).Round(time.Second)))
	}
	logf("=== 全部组件安装成功 ===")
}

// cleanupTask 中断/失败后清理：重连目标机，按顺序执行各组件 Cleanup。
func cleanupTask(t *task) {
	defer func() {
		t.mu.Lock()
		t.running = false
		t.mu.Unlock()
		t.hub.finish()
	}()
	logf := func(line string) { t.hub.publish(line) }

	// 等安装协程退出（连接已关，很快）
	time.Sleep(1 * time.Second)

	p := t.params
	p.ApplyDefaults()
	if p.Password == "" && p.KeyPath == "" {
		if kp := keyPath(); fileExists(kp) {
			p.KeyPath = kp
		}
	}
	logf("--- 重新连接进行清理 ---")
	client, err := sshx.Dial(sshx.Config{
		Host: p.Host, Port: p.Port, User: p.User,
		Password: p.Password, KeyPath: p.KeyPath,
	}, logf)
	if err != nil {
		logf("清理失败（无法重连）: " + err.Error())
		logf("=== 已中断，但痕迹未清理，可稍后用\"清理安装痕迹\"重试 ===")
		return
	}
	defer client.Close()
	for _, inst := range t.insts {
		if err := inst.Cleanup(client, p); err != nil {
			logf(fmt.Sprintf("[清理] %s 清理出错（可忽略或人工检查）: %v", inst.Name(), err))
		}
	}
	_ = client.Run("rm -rf " + p.TmpDir + "/* 2>/dev/null || true")
	logf("=== 已中断并完成清理 ===")
}

// ---------------------------------------------------------------------------
// 免密密钥

func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

func keyPath() string { return filepath.Join(exeDir(), "aksu_key") }

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// ensureKeyPair 生成（或读取已有的）ed25519 密钥对，返回公钥行。
func ensureKeyPair() (string, error) {
	pubFile := keyPath() + ".pub"
	if fileExists(keyPath()) && fileExists(pubFile) {
		data, err := os.ReadFile(pubFile)
		return string(data), err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	block, err := gossh.MarshalPrivateKey(priv, "aksu-installer")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(keyPath(), pem.EncodeToMemory(block), 0600); err != nil {
		return "", err
	}
	sshPub, err := gossh.NewPublicKey(pub)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(gossh.MarshalAuthorizedKey(sshPub))) + " aksu-installer\n"
	if err := os.WriteFile(pubFile, []byte(line), 0644); err != nil {
		return "", err
	}
	return line, nil
}

// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func jsonLine(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func openBrowser(url string) {
	time.Sleep(500 * time.Millisecond)
	var err error
	switch runtime.GOOS {
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	default:
		err = exec.Command("xdg-open", url).Start()
	}
	if err != nil {
		log.Printf("请手动打开浏览器访问 %s", url)
	}
}
