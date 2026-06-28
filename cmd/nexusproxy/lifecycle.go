package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"nexusproxy/internal/config"
)

const defaultRepository = "mananxrobin/NexusProxy"

func runBackground(args []string) error {
	flags := flag.NewFlagSet("nexusproxy run", flag.ContinueOnError)
	configPath := flags.String("config", defaultConfigPath(), "path to config JSON")
	envPathFlag := flags.String("env-file", "", "path to provider key env file")
	pidPath := flags.String("pid-file", defaultStatePath("nexusproxy.pid"), "path to background process pid file")
	logPath := flags.String("log-file", defaultStatePath("nexusproxy.log"), "path to background process log file")
	foreground := flags.Bool("foreground", false, "run in the foreground instead of the background")
	if err := flags.Parse(args); err != nil {
		return err
	}

	envPath := *envPathFlag
	if envPath == "" {
		envPath = defaultEnvPath(*configPath)
	}

	if *foreground {
		return serve(*configPath, envPath)
	}

	if pid, running := runningPID(*pidPath); running {
		fmt.Printf("NexusProxy is already running (pid %d).\n", pid)
		fmt.Printf("Log: %s\n", *logPath)
		return nil
	}

	cfg, err := loadRuntimeConfig(*configPath, envPath)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(*pidPath), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*logPath), 0o755); err != nil {
		return err
	}

	logFile, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()

	executable, err := os.Executable()
	if err != nil {
		return err
	}

	command := exec.Command(executable, "serve", "--config", *configPath, "--env-file", envPath)
	command.Stdout = logFile
	command.Stderr = logFile
	command.Stdin = nil
	command.Env = os.Environ()
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := command.Start(); err != nil {
		return err
	}

	pid := command.Process.Pid
	if err := os.WriteFile(*pidPath, []byte(fmt.Sprintf("%d\n", pid)), 0o600); err != nil {
		_ = command.Process.Signal(syscall.SIGTERM)
		return err
	}
	if err := command.Process.Release(); err != nil {
		return err
	}

	time.Sleep(300 * time.Millisecond)
	if !processRunning(pid) {
		return fmt.Errorf("nexusproxy exited after start; check log: %s", *logPath)
	}

	fmt.Printf("Started NexusProxy (pid %d).\n", pid)
	fmt.Printf("URL: http://%s:%d\n", cfg.Server.Host, cfg.Server.Port)
	fmt.Printf("Log: %s\n", *logPath)
	fmt.Printf("Stop: nexusproxy kill\n")
	return nil
}

func runKill(args []string) error {
	flags := flag.NewFlagSet("nexusproxy kill", flag.ContinueOnError)
	pidPath := flags.String("pid-file", defaultStatePath("nexusproxy.pid"), "path to background process pid file")
	if err := flags.Parse(args); err != nil {
		return err
	}

	pid, err := readPID(*pidPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Println("NexusProxy is not running.")
			return nil
		}
		return err
	}

	if !processRunning(pid) {
		_ = os.Remove(*pidPath)
		fmt.Println("NexusProxy was not running; removed stale pid file.")
		return nil
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processRunning(pid) {
			_ = os.Remove(*pidPath)
			fmt.Println("Stopped NexusProxy.")
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}

	return fmt.Errorf("NexusProxy did not stop within 5 seconds; pid %d is still running", pid)
}

func runUpdate(args []string) error {
	flags := flag.NewFlagSet("nexusproxy update", flag.ContinueOnError)
	version := flags.String("version", getenv("NEXUSPROXY_VERSION", "latest"), "release version to install")
	repository := flags.String("repo", getenv("NEXUSPROXY_REPO", defaultRepository), "GitHub repository in owner/name form")
	if err := flags.Parse(args); err != nil {
		return err
	}

	scriptURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/main/scripts/install.sh", *repository)
	tempFile, err := os.CreateTemp("", "nexusproxy-install-*.sh")
	if err != nil {
		return err
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)

	client := &http.Client{Timeout: 60 * time.Second}
	response, err := client.Get(scriptURL)
	if err != nil {
		_ = tempFile.Close()
		return err
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_ = tempFile.Close()
		return fmt.Errorf("download installer from %s: status %d", scriptURL, response.StatusCode)
	}
	if _, err := io.Copy(tempFile, response.Body); err != nil {
		_ = tempFile.Close()
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}

	command := exec.Command("/bin/sh", tempPath)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Stdin = os.Stdin
	command.Env = append(os.Environ(),
		"NEXUSPROXY_REPO="+*repository,
		"NEXUSPROXY_VERSION="+*version,
	)
	if err := command.Run(); err != nil {
		return err
	}

	if pid, running := runningPID(defaultStatePath("nexusproxy.pid")); running {
		fmt.Printf("Updated binary. NexusProxy is still running as pid %d; restart it with: nexusproxy kill && nexusproxy run\n", pid)
	} else {
		fmt.Println("Updated NexusProxy. Start it with: nexusproxy run")
	}
	return nil
}

func defaultConfigPath() string {
	if value := os.Getenv("NEXUS_CONFIG"); value != "" {
		return value
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		installed := filepath.Join(home, ".config", "nexusproxy", "config.json")
		if _, err := os.Stat(installed); err == nil {
			return installed
		}
		if _, err := os.Stat("config.example.json"); err == nil {
			return "config.example.json"
		}
		return installed
	}
	return "config.example.json"
}

func defaultEnvPath(configPath string) string {
	if value := os.Getenv("NEXUS_ENV_FILE"); value != "" {
		return value
	}
	return config.EnvFilePath(configPath)
}

func defaultStatePath(filename string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config", "nexusproxy", filename)
	}
	return filename
}

func loadRuntimeConfig(configPath string, envPath string) (config.Config, error) {
	if err := config.LoadEnvFile(envPath); err != nil {
		return config.Config{}, fmt.Errorf("load env file %s: %w", envPath, err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return config.Config{}, fmt.Errorf("load config: %w", err)
	}
	return cfg, nil
}

func runningPID(path string) (int, bool) {
	pid, err := readPID(path)
	if err != nil {
		return 0, false
	}
	return pid, processRunning(pid)
}

func readPID(path string) (int, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(bytes)))
	if err != nil {
		return 0, fmt.Errorf("invalid pid file %s: %w", path, err)
	}
	return pid, nil
}

func processRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
