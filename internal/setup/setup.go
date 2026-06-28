package setup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/term"

	"nexusproxy/internal/config"
	"nexusproxy/internal/gateway"
)

type TestMode int

const (
	TestPrompt TestMode = iota
	TestAlways
	TestNever
)

type Options struct {
	ConfigPath string
	EnvPath    string
	Provider   string
	TestMode   TestMode
	In         io.Reader
	Out        io.Writer
	Err        io.Writer

	SecretReader func(prompt string) (string, error)
	HealthCheck  func(context.Context, config.Config) ([]gateway.ProviderStatus, error)
}

type providerGroup struct {
	Type        string
	Name        string
	BaseEnvName string
	HasBaseKey  bool
	EnvNames    map[string]bool
}

type runner struct {
	options       Options
	scanner       *bufio.Scanner
	warnedVisible bool
}

func Run(ctx context.Context, options Options) error {
	if options.ConfigPath == "" {
		return errors.New("config path is required")
	}
	if options.Provider == "" {
		options.Provider = "all"
	}
	if options.In == nil {
		options.In = os.Stdin
	}
	if options.Out == nil {
		options.Out = os.Stdout
	}
	if options.Err == nil {
		options.Err = os.Stderr
	}
	if options.EnvPath == "" {
		options.EnvPath = config.EnvFilePath(options.ConfigPath)
	}
	if options.HealthCheck == nil {
		options.HealthCheck = defaultHealthCheck
	}

	setupRunner := &runner{
		options: options,
		scanner: bufio.NewScanner(options.In),
	}
	return setupRunner.run(ctx)
}

func (runner *runner) run(ctx context.Context) error {
	if err := config.LoadEnvFile(runner.options.EnvPath); err != nil {
		return fmt.Errorf("load env file: %w", err)
	}

	cfg, err := config.Load(runner.options.ConfigPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	groups := providerGroups(cfg, runner.options.Provider)
	if len(groups) == 0 {
		return fmt.Errorf("no providers matched %q", runner.options.Provider)
	}

	fmt.Fprintln(runner.options.Out, "NexusProxy setup")
	fmt.Fprintf(runner.options.Out, "Config: %s\n", runner.options.ConfigPath)
	fmt.Fprintf(runner.options.Out, "Env file: %s\n\n", runner.options.EnvPath)
	fmt.Fprintln(runner.options.Out, "Providers")
	for _, group := range groups {
		status := "missing"
		if group.HasBaseKey {
			status = "saved"
		}
		fmt.Fprintf(runner.options.Out, "- %s (%s): %s\n", group.Name, group.BaseEnvName, status)
	}
	fmt.Fprintln(runner.options.Out)

	values := map[string]string{}
	for index := range groups {
		group := &groups[index]
		if !group.HasBaseKey {
			value, err := runner.readSecret(fmt.Sprintf("Enter %s API key [blank to skip]: ", group.Name))
			if err != nil {
				return err
			}
			if value != "" {
				values[group.BaseEnvName] = value
				group.EnvNames[group.BaseEnvName] = true
				group.HasBaseKey = true
			}
		}

		if err := runner.promptExtraKeys(group, values); err != nil {
			return err
		}
	}

	if len(values) > 0 {
		if err := config.SaveEnvValues(runner.options.EnvPath, values); err != nil {
			return fmt.Errorf("save env file: %w", err)
		}
		if err := config.ApplyEnvValues(values); err != nil {
			return fmt.Errorf("apply env values: %w", err)
		}
		fmt.Fprintf(runner.options.Out, "\nSaved %s to %s.\n", pluralizeKeys(len(values)), runner.options.EnvPath)
		fmt.Fprintln(runner.options.Out, "If NexusProxy is already running, restart it to load CLI-saved keys, or use dashboard for hot-load.")
	} else {
		fmt.Fprintln(runner.options.Out, "\nNo new keys saved.")
	}

	shouldTest, err := runner.shouldTest(values)
	if err != nil {
		return err
	}
	if shouldTest {
		if err := runner.runHealthCheck(ctx); err != nil {
			return err
		}
	}

	return nil
}

func (runner *runner) promptExtraKeys(group *providerGroup, values map[string]string) error {
	for {
		add, err := runner.confirm(fmt.Sprintf("Add another %s key? [y/N]: ", group.Name))
		if err != nil {
			return err
		}
		if !add {
			return nil
		}

		nextEnv := nextEnvName(group.BaseEnvName, group.EnvNames)
		value, err := runner.readSecret(fmt.Sprintf("Enter another %s API key for %s [blank to cancel]: ", group.Name, nextEnv))
		if err != nil {
			return err
		}
		if value == "" {
			return nil
		}

		values[nextEnv] = value
		group.EnvNames[nextEnv] = true
	}
}

func (runner *runner) shouldTest(values map[string]string) (bool, error) {
	switch runner.options.TestMode {
	case TestAlways:
		return true, nil
	case TestNever:
		return false, nil
	default:
		if len(values) == 0 {
			return false, nil
		}
		return runner.confirm("Test provider keys now? This runs one small search per ready provider. [y/N]: ")
	}
}

func (runner *runner) runHealthCheck(ctx context.Context) error {
	cfg, err := config.Load(runner.options.ConfigPath)
	if err != nil {
		return fmt.Errorf("reload config before test: %w", err)
	}

	statuses, err := runner.options.HealthCheck(ctx, cfg)
	if err != nil {
		return fmt.Errorf("test provider keys: %w", err)
	}

	fmt.Fprintln(runner.options.Out, "\nProvider test")
	fmt.Fprintf(runner.options.Out, "%-24s %-14s %-12s %s\n", "Provider", "State", "Last Status", "Last Error")
	for _, status := range statuses {
		if !providerMatches(status.Type, runner.options.Provider) {
			continue
		}
		state := "ready"
		if !status.Usable {
			state = status.Reason
		}
		lastStatus := "-"
		if status.Stats.LastStatus != 0 {
			lastStatus = strconv.Itoa(status.Stats.LastStatus)
		}
		lastError := status.Stats.LastError
		if lastError == "" {
			lastError = "-"
		}
		fmt.Fprintf(runner.options.Out, "%-24s %-14s %-12s %s\n", status.ID, state, lastStatus, lastError)
	}
	return nil
}

func (runner *runner) confirm(prompt string) (bool, error) {
	answer, err := runner.readLine(prompt)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func (runner *runner) readLine(prompt string) (string, error) {
	fmt.Fprint(runner.options.Out, prompt)
	if !runner.scanner.Scan() {
		if err := runner.scanner.Err(); err != nil {
			return "", err
		}
		return "", io.EOF
	}
	return strings.TrimSpace(runner.scanner.Text()), nil
}

func (runner *runner) readSecret(prompt string) (string, error) {
	fmt.Fprint(runner.options.Out, prompt)
	if runner.options.SecretReader != nil {
		value, err := runner.options.SecretReader(prompt)
		fmt.Fprintln(runner.options.Out)
		return strings.TrimSpace(value), err
	}

	if file, ok := runner.options.In.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		bytes, err := term.ReadPassword(int(file.Fd()))
		fmt.Fprintln(runner.options.Out)
		return strings.TrimSpace(string(bytes)), err
	}

	if !runner.warnedVisible {
		fmt.Fprintln(runner.options.Err, "warning: input is not a terminal; API key input will be visible")
		runner.warnedVisible = true
	}
	if !runner.scanner.Scan() {
		if err := runner.scanner.Err(); err != nil {
			return "", err
		}
		return "", io.EOF
	}
	return strings.TrimSpace(runner.scanner.Text()), nil
}

func providerGroups(cfg config.Config, providerFilter string) []providerGroup {
	byBase := map[string]*providerGroup{}
	order := []string{}

	for _, provider := range cfg.Providers {
		if provider.APIKeyEnv == "" || !providerMatches(provider.Type, providerFilter) {
			continue
		}

		baseEnv := baseEnvName(provider.APIKeyEnv)
		group, exists := byBase[baseEnv]
		if !exists {
			group = &providerGroup{
				Type:        provider.Type,
				Name:        providerDisplayName(provider.Type),
				BaseEnvName: baseEnv,
				EnvNames:    map[string]bool{},
			}
			byBase[baseEnv] = group
			order = append(order, baseEnv)
		}

		group.EnvNames[provider.APIKeyEnv] = true
		if provider.APIKeyEnv == baseEnv && provider.APIKey != "" {
			group.HasBaseKey = true
		}
	}

	sort.SliceStable(order, func(left, right int) bool {
		leftGroup := byBase[order[left]]
		rightGroup := byBase[order[right]]
		if leftGroup.Name != rightGroup.Name {
			return leftGroup.Name < rightGroup.Name
		}
		return leftGroup.BaseEnvName < rightGroup.BaseEnvName
	})

	groups := make([]providerGroup, 0, len(order))
	for _, baseEnv := range order {
		groups = append(groups, *byBase[baseEnv])
	}
	return groups
}

func providerMatches(providerType string, filter string) bool {
	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" || filter == "all" {
		return true
	}
	return strings.EqualFold(providerType, filter)
}

func providerDisplayName(providerType string) string {
	switch strings.ToLower(strings.TrimSpace(providerType)) {
	case "brave":
		return "Brave Search"
	case "tavily":
		return "Tavily"
	case "serper":
		return "Serper"
	default:
		parts := strings.FieldsFunc(providerType, func(char rune) bool {
			return char == '-' || char == '_' || char == ' '
		})
		for index, part := range parts {
			if part == "" {
				continue
			}
			parts[index] = strings.ToUpper(part[:1]) + strings.ToLower(part[1:])
		}
		name := strings.Join(parts, " ")
		if name == "" {
			return "Custom Provider"
		}
		return name
	}
}

func baseEnvName(envName string) string {
	index := strings.LastIndex(envName, "_")
	if index < 0 {
		return envName
	}

	suffix := envName[index+1:]
	if suffix == "" {
		return envName
	}
	for _, char := range suffix {
		if char < '0' || char > '9' {
			return envName
		}
	}

	if parsed, err := strconv.Atoi(suffix); err == nil && parsed >= 2 {
		return envName[:index]
	}
	return envName
}

func nextEnvName(baseEnv string, envNames map[string]bool) string {
	next := 2
	for envName := range envNames {
		index, ok := config.APIKeyEnvIndex(baseEnv, envName)
		if ok && index >= next {
			next = index + 1
		}
	}
	return fmt.Sprintf("%s_%d", baseEnv, next)
}

func defaultHealthCheck(ctx context.Context, cfg config.Config) ([]gateway.ProviderStatus, error) {
	searchGateway := gateway.New(cfg, gateway.Options{
		Client: &http.Client{},
	})
	return searchGateway.RefreshProviderHealth(ctx, ""), nil
}

func pluralizeKeys(count int) string {
	if count == 1 {
		return "1 key"
	}
	return fmt.Sprintf("%d keys", count)
}
