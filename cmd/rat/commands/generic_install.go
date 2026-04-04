package commands

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/maximerivest/rat/internal/daemon"
	"github.com/maximerivest/rat/internal/generic"
	"github.com/maximerivest/rat/internal/state"
)

type genericInstallCheck struct {
	Lang            string
	ConfigPath      string
	ConfigDir       string
	Config          *generic.RuntimeConfig
	RuntimeBinary   string
	RuntimeError    error
	ExtraCommands   map[string]string
	MissingCommands []string
	MissingEnv      []string
	StateDir        string
	CacheDir        string
	ConfigWritable  bool
	CacheWritable   bool
}

func installGenericRuntime(lang string) error {
	check, err := inspectGenericRuntime(lang)
	if err != nil {
		return err
	}

	fmt.Printf("rat install %s\n", lang)
	fmt.Printf("OS: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Println("")
	statusLine("runtime", true, fmt.Sprintf("%s (%s)", check.Config.Display, check.ConfigPath))
	statusLine("binary", check.RuntimeError == nil, valueOrNote(check.RuntimeBinary, errorText(check.RuntimeError, "not found")))
	for _, cmd := range check.Config.Install.CheckCommands {
		path := check.ExtraCommands[cmd]
		statusLine(cmd, path != "", valueOrNote(path, "not found"))
	}
	for _, env := range check.Config.Install.CheckEnv {
		_, ok := os.LookupEnv(env)
		statusLine(env, ok, valueOrNote(os.Getenv(env), "not set"))
	}
	statusLine("config dir", check.ConfigWritable, check.StateDir)
	statusLine("cache dir", check.CacheWritable, check.CacheDir)

	runtimeStep := check.Config.RuntimeInstallStep()
	frontendStep := check.Config.FrontendInstallStep()

	missingRuntimeFatal := check.RuntimeError != nil && !canInstallMissingRuntime(runtimeStep)
	if !check.ConfigWritable || !check.CacheWritable || missingRuntimeFatal || len(check.MissingCommands) > 0 || len(check.MissingEnv) > 0 {
		if hint := genericInstallHint(check); hint != "" {
			fmt.Println("")
			fmt.Println("Suggested fix:")
			fmt.Printf("  %s\n", hint)
		}
		if len(check.MissingEnv) > 0 {
			fmt.Println("")
			fmt.Printf("Set required environment variables: %s\n", strings.Join(check.MissingEnv, ", "))
		}
		return fmt.Errorf("%s install incomplete: missing prerequisites", lang)
	}

	if len(runtimeStep.Deps) > 0 {
		fmt.Println("")
		fmt.Printf("Installing %s runtime deps...\n", check.Config.Display)
		if err := installGenericStep(runtimeStep, check, "runtime"); err != nil {
			return err
		}
	}

	if len(frontendStep.Deps) > 0 {
		fmt.Println("")
		fmt.Println("Installing frontend deps...")
		if err := installGenericStep(frontendStep, check, "frontend"); err != nil {
			return err
		}
	}

	if check.RuntimeError != nil && canInstallMissingRuntime(runtimeStep) {
		check.RuntimeBinary, check.RuntimeError = check.Config.DetectBinary()
		if check.RuntimeError != nil {
			return fmt.Errorf("%s install completed but runtime binary is still missing: %w", lang, check.RuntimeError)
		}
	}

	r, err := resolveInput(lang)
	if err != nil {
		return err
	}

	k, err := daemon.Start(store(), daemon.StartOpts{
		Name:        r.Name,
		Lang:        r.Lang,
		Cwd:         r.Cwd,
		Venv:        r.Venv,
		RuntimePath: check.RuntimeBinary,
	})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	statusText, runText, err := smokeTestGenericRuntime(ctx, r.Name, check)
	if err != nil {
		return err
	}

	fmt.Println("")
	statusLine("kernel", true, fmt.Sprintf("http://127.0.0.1:%d/mcp", k.Port))
	if statusText != "" {
		statusLine("smoke", true, statusText)
	}
	if runText != "" {
		fmt.Println("")
		fmt.Println(runText)
	}
	fmt.Println("")
	fmt.Println("Ready.")
	fmt.Println("Try:")
	fmt.Printf("  rat %s\n", lang)
	if check.Config.Install.Smoke.Run != "" {
		fmt.Printf("  rat run %s %q\n", lang, check.Config.Install.Smoke.Run)
	} else {
		fmt.Printf("  rat look %s\n", lang)
	}
	fmt.Printf("  rat look %s\n", lang)
	return nil
}

func inspectGenericRuntime(lang string) (genericInstallCheck, error) {
	configPath, err := findRuntimeConfig(lang)
	if err != nil {
		return genericInstallCheck{}, err
	}
	cfg, err := generic.LoadConfig(configPath)
	if err != nil {
		return genericInstallCheck{}, err
	}

	check := genericInstallCheck{
		Lang:          lang,
		ConfigPath:    configPath,
		ConfigDir:     filepath.Dir(configPath),
		Config:        cfg,
		ExtraCommands: map[string]string{},
		StateDir:      filepath.Dir(state.DefaultPath()),
		CacheDir:      defaultCacheDir(),
	}
	check.ConfigWritable = dirWritable(check.StateDir)
	check.CacheWritable = dirWritable(check.CacheDir)
	check.RuntimeBinary, check.RuntimeError = cfg.DetectBinary()

	for _, cmd := range cfg.Install.CheckCommands {
		path, err := exec.LookPath(cmd)
		if err == nil {
			check.ExtraCommands[cmd] = path
			continue
		}
		check.MissingCommands = append(check.MissingCommands, cmd)
	}
	for _, env := range cfg.Install.CheckEnv {
		if _, ok := os.LookupEnv(env); !ok {
			check.MissingEnv = append(check.MissingEnv, env)
		}
	}
	return check, nil
}

func installGenericStep(step generic.InstallStep, check genericInstallCheck, phase string) error {
	switch step.Manager {
	case "", "none":
		return nil
	case "r":
		missing, err := missingRPackages(check.RuntimeBinary, step.Deps)
		if err != nil {
			return err
		}
		if len(missing) == 0 {
			return nil
		}
		fmt.Printf("  installing R packages: %s\n", strings.Join(missing, ", "))
		if err := installRPackages(check.RuntimeBinary, missing); err != nil {
			return err
		}
		missing, err = missingRPackages(check.RuntimeBinary, step.Deps)
		if err != nil {
			return err
		}
		if len(missing) > 0 {
			return fmt.Errorf("%s deps still missing after install: %s", phase, strings.Join(missing, ", "))
		}
		return nil
	case "pip":
		py, err := detectPipPython()
		if err != nil {
			return err
		}
		missing := missingPipPackages(py, step.Deps)
		if len(missing) == 0 {
			return nil
		}
		fmt.Printf("  installing pip packages: %s\n", strings.Join(missing, ", "))
		if err := installPipPackages(py, missing); err != nil {
			return err
		}
		missing = missingPipPackages(py, step.Deps)
		if len(missing) > 0 {
			return fmt.Errorf("%s deps still missing after install: %s", phase, strings.Join(missing, ", "))
		}
		return nil
	case "npm":
		npm, err := detectNPM()
		if err != nil {
			return err
		}
		fmt.Printf("  installing npm packages: %s\n", strings.Join(step.Deps, ", "))
		if err := installNPMPackages(npm, step.Deps); err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unsupported install manager %q", step.Manager)
	}
}

func smokeTestGenericRuntime(ctx context.Context, name string, check genericInstallCheck) (statusText, runText string, err error) {
	session, err := connectToKernel(ctx, name)
	if err != nil {
		return "", "", err
	}
	defer session.Close()

	smoke := check.Config.Install.Smoke
	if smoke.Ctl == "" && smoke.Run == "" {
		smoke.Ctl = "status"
	}

	if smoke.Ctl != "" {
		result, err := session.Ctl(ctx, smoke.Ctl)
		if err != nil {
			return "", "", err
		}
		statusText = strings.TrimSpace(extractText(result))
		statusText = strings.SplitN(statusText, "\n", 2)[0]
	}

	if smoke.Run != "" {
		result, err := session.Run(ctx, smoke.Run)
		if err != nil {
			return statusText, "", err
		}
		runText = strings.TrimSpace(extractText(result))
		if smoke.Expect != "" && !strings.Contains(runText, smoke.Expect) {
			return statusText, runText, fmt.Errorf("smoke test output mismatch: want %q in %q", smoke.Expect, runText)
		}
	}

	return statusText, runText, nil
}

func genericInstallHint(check genericInstallCheck) string {
	var cmds []string
	if check.RuntimeError != nil {
		cmds = append(cmds, check.Config.Detect.Commands...)
	}
	cmds = append(cmds, check.MissingCommands...)
	cmds = uniqStrings(cmds)
	if len(cmds) == 0 {
		return ""
	}
	if runtime.GOOS == "windows" && contains(cmds, "tmux") {
		return "Install WSL, then install the missing commands inside WSL and run rat there."
	}
	switch detectPackageManager() {
	case "brew":
		return "brew install " + strings.Join(cmds, " ")
	case "apt-get":
		return "sudo apt-get update && sudo apt-get install -y " + strings.Join(cmds, " ")
	case "dnf":
		return "sudo dnf install -y " + strings.Join(cmds, " ")
	case "yum":
		return "sudo yum install -y " + strings.Join(cmds, " ")
	case "pacman":
		return "sudo pacman -S --needed " + strings.Join(cmds, " ")
	case "zypper":
		return "sudo zypper install -y " + strings.Join(cmds, " ")
	case "apk":
		return "sudo apk add " + strings.Join(cmds, " ")
	default:
		return "Install missing commands: " + strings.Join(cmds, ", ")
	}
}

func missingRPackages(binary string, pkgs []string) ([]string, error) {
	if len(pkgs) == 0 {
		return nil, nil
	}
	expr := fmt.Sprintf("pkgs <- c(%s); missing <- pkgs[!vapply(pkgs, requireNamespace, logical(1), quietly=TRUE)]; if (length(missing)) cat(paste(missing, collapse='\\n'))", rStringVector(pkgs))
	out, err := runR(binary, expr)
	if err != nil {
		return nil, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

func installRPackages(binary string, pkgs []string) error {
	expr := fmt.Sprintf("options(repos = c(CRAN = 'https://cloud.r-project.org')); install.packages(c(%s), quiet=TRUE)", rStringVector(pkgs))
	return runRStreaming(binary, expr)
}

func runR(binary, expr string) (string, error) {
	var buf bytes.Buffer
	cmd := exec.Command(binary, rExprArgs(binary, expr)...)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %w\n%s", filepath.Base(binary), err, strings.TrimSpace(buf.String()))
	}
	return buf.String(), nil
}

func runRStreaming(binary, expr string) error {
	cmd := exec.Command(binary, rExprArgs(binary, expr)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(binary), err)
	}
	return nil
}

func rExprArgs(binary, expr string) []string {
	base := strings.ToLower(filepath.Base(binary))
	if strings.HasPrefix(base, "rscript") {
		return []string{"-e", expr}
	}
	return []string{"--slave", "-e", expr}
}

func rStringVector(pkgs []string) string {
	quoted := make([]string, 0, len(pkgs))
	for _, pkg := range pkgs {
		quoted = append(quoted, fmt.Sprintf("'%s'", strings.ReplaceAll(pkg, "'", "\\\\'")))
	}
	return strings.Join(quoted, ", ")
}

func canInstallMissingRuntime(step generic.InstallStep) bool {
	switch step.Manager {
	case "npm":
		return len(step.Deps) > 0
	default:
		return false
	}
}

func detectPipPython() (string, error) {
	for _, name := range []string{"python3", "python"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("python3 not found (required for pip-managed frontend deps)")
}

func missingPipPackages(py string, pkgs []string) []string {
	var missing []string
	for _, pkg := range pkgs {
		if !canImport(py, pipImportName(pkg)) {
			missing = append(missing, pkg)
		}
	}
	return missing
}

func installPipPackages(py string, pkgs []string) error {
	if uv, err := exec.LookPath("uv"); err == nil {
		args := []string{"pip", "install", "--python", py}
		args = append(args, pkgs...)
		cmd := exec.Command(uv, args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("uv pip install: %w", err)
		}
		return nil
	}

	args := []string{"-m", "pip", "install"}
	if os.Getenv("VIRTUAL_ENV") == "" {
		args = append(args, "--user")
	}
	args = append(args, pkgs...)
	cmd := exec.Command(py, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pip install: %w", err)
	}
	return nil
}

func detectNPM() (string, error) {
	if path, err := exec.LookPath("npm"); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("npm not found (required for npm-managed runtime deps)")
}

func installNPMPackages(npm string, pkgs []string) error {
	args := []string{"install", "-g"}
	args = append(args, pkgs...)
	cmd := exec.Command(npm, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("npm install -g: %w", err)
	}
	return nil
}

func pipImportName(pkg string) string {
	return strings.ReplaceAll(pkg, "-", "_")
}

func uniqStrings(items []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

func contains(items []string, needle string) bool {
	for _, item := range items {
		if item == needle {
			return true
		}
	}
	return false
}

func errorText(err error, fallback string) string {
	if err == nil {
		return fallback
	}
	return err.Error()
}
