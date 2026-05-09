package assets

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/beck-8/subs-check/config"
	"github.com/beck-8/subs-check/save/method"
	"github.com/klauspost/compress/zstd"
	"gopkg.in/natefinch/lumberjack.v2"
)

func ResolveNodePath() (string, error) {
	if nodeBinPath := strings.TrimSpace(os.Getenv("NODEBIN_PATH")); nodeBinPath != "" {
		if _, err := os.Stat(nodeBinPath); err != nil {
			return "", fmt.Errorf("NODEBIN_PATH=%q 不可用: %w", nodeBinPath, err)
		}
		return nodeBinPath, nil
	}

	nodePath, err := exec.LookPath("node")
	if err != nil {
		return "", fmt.Errorf("未找到 node，可安装 Node.js 或设置 NODEBIN_PATH")
	}
	return nodePath, nil
}

func RunSubStoreService(nodePath string) {
	for {
		if err := startSubStore(nodePath); err != nil {
			slog.Error("Sub-store service crashed, restarting...", "error", err)
		}
		time.Sleep(time.Second * 30)
	}
}

func startSubStore(nodePath string) error {
	saver, err := method.NewLocalSaver()
	if err != nil {
		return err
	}
	if !filepath.IsAbs(saver.OutputPath) {
		// 处理用户写相对路径的问题
		saver.OutputPath = filepath.Join(saver.BasePath, saver.OutputPath)
	}

	os.MkdirAll(saver.OutputPath, 0755)
	jsPath := filepath.Join(saver.OutputPath, "sub-store.bundle.js")
	overYamlPath := filepath.Join(saver.OutputPath, "ACL4SSR_Online_Full.yaml")
	logPath := filepath.Join(saver.OutputPath, "sub-store.log")

	os.Remove(jsPath)
	os.Remove(overYamlPath)
	if err := decodeZstd(jsPath, overYamlPath); err != nil {
		return err
	}

	// 配置日志轮转
	logWriter := &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    10, // 每个日志文件最大 10MB
		MaxBackups: 3,  // 保留 3 个旧文件
		MaxAge:     7,  // 保留 7 天
	}
	defer logWriter.Close()

	// 支持自定义sub-store脚本路径
	if subStoreBinPath := os.Getenv("SUB_STORE_PATH"); subStoreBinPath != "" {
		jsPath = subStoreBinPath
	}
	// 运行 JavaScript 文件
	cmd := exec.Command(nodePath, jsPath)
	// js会在运行目录释放依赖文件
	cmd.Dir = saver.OutputPath
	cmd.Stdout = logWriter
	cmd.Stderr = logWriter
	cmd.Env = os.Environ()

	// 检查MihomoOverwriteUrl是否包含本地IP，如果是则移除代理环境变量
	cleanProxyEnv := false
	if config.GlobalConfig.MihomoOverwriteUrl != "" {
		parsedURL, err := url.Parse(config.GlobalConfig.MihomoOverwriteUrl)
		if err == nil {
			host := parsedURL.Hostname()
			if isLocalIP(host) {
				cleanProxyEnv = true
				slog.Debug("MihomoOverwriteUrl contains local IP, removing proxy environment variables")
			}
		}
	}

	// ipv4/ipv6 都支持
	hostPort := strings.Split(config.GlobalConfig.SubStorePort, ":")
	// host可以为空，port不能为空
	if len(hostPort) == 2 && hostPort[1] != "" {
		cmd.Env = append(cmd.Env,
			fmt.Sprintf("SUB_STORE_BACKEND_API_HOST=%s", hostPort[0]),
			fmt.Sprintf("SUB_STORE_BACKEND_API_PORT=%s", hostPort[1]),
		)
	} else if len(hostPort) == 1 {
		cmd.Env = append(cmd.Env, fmt.Sprintf("SUB_STORE_BACKEND_API_PORT=%s", hostPort[0])) // 设置端口
	} else {
		return fmt.Errorf("sub-store-port invalid port format: %s", config.GlobalConfig.SubStorePort)
	}

	// https://hub.docker.com/r/xream/sub-store
	// 这里有详细的变量说明，可能用NO_PROXY过滤到127.0.0.1更合适
	// 如果MihomoOverwriteUrl包含本地IP，则移除所有代理环境变量
	if cleanProxyEnv {
		filteredEnv := make([]string, 0, len(cmd.Env))
		proxyVars := []string{"http_proxy", "https_proxy", "all_proxy", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"}

		for _, env := range cmd.Env {
			isProxyVar := false
			for _, proxyVar := range proxyVars {
				if strings.HasPrefix(strings.ToLower(env), strings.ToLower(proxyVar)+"=") {
					isProxyVar = true
					break
				}
			}
			if !isProxyVar {
				filteredEnv = append(filteredEnv, env)
			}
		}
		cmd.Env = filteredEnv
	}

	// 增加body限制，默认1M
	if os.Getenv("SUB_STORE_BODY_JSON_LIMIT") == "" {
		cmd.Env = append(cmd.Env, "SUB_STORE_BODY_JSON_LIMIT=30mb")
	}
	// 增加自定义访问路径
	if config.GlobalConfig.SubStorePath != "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("SUB_STORE_FRONTEND_BACKEND_PATH=%s", config.GlobalConfig.SubStorePath))
		cmd.Env = append(cmd.Env, "SUB_STORE_BACKEND_MERGE=1")
	}

	// sub-store 环境变量: 后端上传文件至 gist
	if config.GlobalConfig.SubStoreSyncCron != "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("SUB_STORE_BACKEND_SYNC_CRON=%s", config.GlobalConfig.SubStoreSyncCron))
	}

	// sub-store 环境变量: 自动拉取订阅内容
	if config.GlobalConfig.SubStoreProduceCron != "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("SUB_STORE_PRODUCE_CRON=%s", config.GlobalConfig.SubStoreProduceCron))
	}

	// sub-store 环境变量: 当遇到错误时发送通知
	if config.GlobalConfig.SubStorePushService != "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("SUB_STORE_PUSH_SERVICE=%s", config.GlobalConfig.SubStorePushService))
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 sub-store 失败: %w", err)
	}

	slog.Info("Sub-store service started", "pid", cmd.Process.Pid, "port", config.GlobalConfig.SubStorePort, "log", logPath)

	// 等待程序结束
	return cmd.Wait()
}

// isLocalIP 检查IP是否是本地IP（127.0.0.1或局域网IP）
func isLocalIP(host string) bool {
	// 检查是否是localhost或127.0.0.1
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}

	// 检查IP是否有效
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}

	// 检查是否是私有IP范围
	privateIPBlocks := []string{
		"10.0.0.0/8",     // 10.0.0.0 - 10.255.255.255
		"172.16.0.0/12",  // 172.16.0.0 - 172.31.255.255
		"192.168.0.0/16", // 192.168.0.0 - 192.168.255.255
		"169.254.0.0/16", // 169.254.0.0 - 169.254.255.255
		"fd00::/8",       // fd00:: - fdff:ffff:ffff:ffff:ffff:ffff:ffff:ffff
	}

	for _, block := range privateIPBlocks {
		_, ipNet, err := net.ParseCIDR(block)
		if err != nil {
			continue
		}
		if ipNet.Contains(ip) {
			return true
		}
	}

	return false
}

func decodeZstd(jsPath, overYamlPath string) error {
	// 创建 zstd 解码器
	zstdDecoder, err := zstd.NewReader(nil)
	if err != nil {
		return fmt.Errorf("创建zstd解码器失败: %w", err)
	}
	defer zstdDecoder.Close()

	// 解压 sub-store 脚本
	jsFile, err := os.OpenFile(jsPath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("创建 sub-store 脚本文件失败: %w", err)
	}
	defer jsFile.Close()

	zstdDecoder.Reset(bytes.NewReader(EmbeddedSubStore))
	if _, err := io.Copy(jsFile, zstdDecoder); err != nil {
		return fmt.Errorf("解压 sub-store 脚本失败: %w", err)
	}

	// 解压 覆写文件
	overYamlFile, err := os.OpenFile(overYamlPath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("创建 ACL4SSR_Online_Full.yaml 文件失败: %w", err)
	}
	defer overYamlFile.Close()

	zstdDecoder.Reset(bytes.NewReader(EmbeddedOverrideYaml))
	if _, err := io.Copy(overYamlFile, zstdDecoder); err != nil {
		return fmt.Errorf("解压 ACL4SSR_Online_Full.yaml 失败: %w", err)
	}
	return nil
}
