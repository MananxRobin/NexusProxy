package main

import (
	"bufio"
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

	return stopBackground(*pidPath, os.Stdout)
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

func runUninstall(args []string) error {
	flags := flag.NewFlagSet("nexusproxy uninstall", flag.ContinueOnError)
	binaryPath := flags.String("binary", "", "path to NexusProxy binary to remove")
	configDir := flags.String("config-dir", defaultConfigDir(), "path to NexusProxy config directory")
	pidPath := flags.String("pid-file", defaultStatePath("nexusproxy.pid"), "path to background process pid file")
	purge := flags.Bool("purge", false, "remove config and provider API keys too")
	yes := flags.Bool("yes", false, "skip confirmation prompts")
	if err := flags.Parse(args); err != nil {
		return err
	}

	if *binaryPath == "" {
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		*binaryPath = executable
	}

	return uninstall(uninstallOptions{
		BinaryPath: *binaryPath,
		ConfigDir:  *configDir,
		PIDPath:    *pidPath,
		Purge:      *purge,
		Yes:        *yes,
		In:         os.Stdin,
		Out:        os.Stdout,
	})
}

type uninstallOptions struct {
	BinaryPath string
	ConfigDir  string
	PIDPath    string
	Purge      bool
	Yes        bool
	In         io.Reader
	Out        io.Writer
}

func uninstall(options uninstallOptions) error {
	if options.In == nil {
		options.In = os.Stdin
	}
	if options.Out == nil {
		options.Out = os.Stdout
	}
	if options.BinaryPath == "" {
		return errors.New("binary path is required")
	}
	if options.ConfigDir == "" || options.ConfigDir == "." || options.ConfigDir == string(filepath.Separator) {
		return fmt.Errorf("refusing to use unsafe config directory %q", options.ConfigDir)
	}

	if err := stopBackground(options.PIDPath, options.Out); err != nil {
		return err
	}

	removedBinary, err := removeFileIfExists(options.BinaryPath)
	if err != nil {
		return fmt.Errorf("remove binary %s: %w", options.BinaryPath, err)
	}
	if removedBinary {
		fmt.Fprintf(options.Out, "Removed binary: %s\n", options.BinaryPath)
	} else {
		fmt.Fprintf(options.Out, "Binary was already removed: %s\n", options.BinaryPath)
	}

	if !options.Purge {
		fmt.Fprintf(options.Out, "Kept config and API keys: %s\n", options.ConfigDir)
		fmt.Fprintln(options.Out, "Run nexusproxy uninstall --purge to remove them too.")
		return nil
	}

	if !options.Yes {
		confirmed, err := confirmPurge(options.In, options.Out, options.ConfigDir)
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Fprintf(options.Out, "Kept config and API keys: %s\n", options.ConfigDir)
			return nil
		}
	}

	removedConfig, err := removeDirIfExists(options.ConfigDir)
	if err != nil {
		return fmt.Errorf("remove config directory %s: %w", options.ConfigDir, err)
	}
	if removedConfig {
		fmt.Fprintf(options.Out, "Removed config and API keys: %s\n", options.ConfigDir)
	} else {
		fmt.Fprintf(options.Out, "Config directory was already removed: %s\n", options.ConfigDir)
	}
	return nil
}

func stopBackground(pidPath string, out io.Writer) error {
	pid, err := readPID(pidPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(out, "NexusProxy is not running.")
			return nil
		}
		return err
	}

	if !processRunning(pid) {
		_ = os.Remove(pidPath)
		fmt.Fprintln(out, "NexusProxy was not running; removed stale pid file.")
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
			_ = os.Remove(pidPath)
			fmt.Fprintln(out, "Stopped NexusProxy.")
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}

	return fmt.Errorf("NexusProxy did not stop within 5 seconds; pid %d is still running", pid)
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

func defaultConfigDir() string {
	if value := os.Getenv("NEXUS_CONFIG"); value != "" {
		return filepath.Dir(value)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config", "nexusproxy")
	}
	return filepath.Dir(defaultConfigPath())
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

func confirmPurge(in io.Reader, out io.Writer, configDir string) (bool, error) {
	fmt.Fprintf(out, "Remove %s including provider API keys? [y/N]: ", configDir)
	scanner := bufio.NewScanner(in)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return false, err
		}
		return false, nil
	}

	switch strings.ToLower(strings.TrimSpace(scanner.Text())) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func removeFileIfExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if info.IsDir() {
		return false, fmt.Errorf("path is a directory")
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	return true, nil
}

func removeDirIfExists(path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if err := os.RemoveAll(path); err != nil {
		return false, err
	}
	return true, nil
}
