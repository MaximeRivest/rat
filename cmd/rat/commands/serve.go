package commands

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"

	"github.com/maximerivest/rat/internal/activity"
	"github.com/maximerivest/rat/internal/bash"
	"github.com/maximerivest/rat/internal/generic"
	"github.com/maximerivest/rat/internal/kernel"
	"github.com/maximerivest/rat/internal/mcpserver"
	"github.com/maximerivest/rat/internal/python"
	"github.com/maximerivest/rat/internal/runtimes"
)

var (
	serveHTTPFlag    bool
	servePort        int
	serveCwd         string
	serveLangFlag    string
	serveNameFlag    string
	serveVenvFlag    string
	serveRuntimeFlag string
	serveOptFlags    []string
	serveEnvFlags    []string
)

func init() {
	serveCmd.Flags().BoolVar(&serveHTTPFlag, "http", false, "Run as HTTP server (default: stdio)")
	serveCmd.Flags().IntVar(&servePort, "port", 8720, "HTTP port")
	serveCmd.Flags().StringVar(&serveCwd, "cwd", "", "Working directory (default: current)")
	serveCmd.Flags().StringVar(&serveLangFlag, "lang", "", "Canonical language (for named runtimes)")
	serveCmd.Flags().StringVar(&serveNameFlag, "kernel-name", "", "Runtime name recorded in state (default: first arg)")
	serveCmd.Flags().StringVar(&serveVenvFlag, "venv", "", "Python venv path (py only)")
	serveCmd.Flags().StringVar(&serveRuntimeFlag, "runtime", "", "Path to language binary (e.g. /opt/python3.11/bin/python3)")
	serveCmd.Flags().StringArrayVar(&serveOptFlags, "opt", nil, "Structured runtime options (KEY=VALUE, repeatable)")
	serveCmd.Flags().StringArrayVar(&serveEnvFlags, "env", nil, "Extra env vars for the kernel (KEY=VALUE, repeatable)")

	rootCmd.AddCommand(serveCmd)
}

var serveCmd = &cobra.Command{
	Use:     "serve <name> [--http] [--port PORT]",
	Short:   "MCP server (for app builders)",
	GroupID: "setup",
	Long: `Run an MCP server in the foreground.

This is the low-level building block behind every rat kernel. By
default, it runs as a stdio server. With --http, it runs as an HTTP
server for shared access from multiple clients.

Use --runtime to specify an exact interpreter binary, bypassing
auto-detection. This is passed through automatically when you start
a kernel created with 'rat add --runtime'.

Examples:
  rat serve sh              # MCP stdio server for bash
  rat serve sh --http       # MCP HTTP server on :8720
  rat serve py --http --port 8717 --cwd ~/project
  rat serve py --http --runtime /opt/python3.11/bin/python3`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runServe(args[0])
	},
}

func runServe(input string) error {
	name := input
	if serveNameFlag != "" {
		name = serveNameFlag
	}

	lang := serveLangFlag
	var err error
	if lang == "" {
		lang, err = resolveLang(input)
		if err != nil {
			return err
		}
	}

	cwd := serveCwd
	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("getwd: %w", err)
		}
	}
	cwd, _ = filepath.Abs(cwd)

	// Apply extra env vars (from --env flags / rat add --env).
	envMap := parseKVFlags(serveEnvFlags)
	for k, v := range envMap {
		os.Setenv(k, v)
	}
	optionsMap := parseKVFlags(serveOptFlags)

	// Create the kernel for the requested language.
	var k kernel.Kernel

	switch lang {
	case "sh":
		k, err = bash.New(name, cwd)
	case "py":
		k, err = python.New(name, cwd, serveVenvFlag, serveRuntimeFlag)
	default:
		// Try to load a user-defined or community runtime.
		k, err = loadGenericKernel(name, lang, cwd, serveRuntimeFlag, optionsMap)
	}

	if err != nil {
		return fmt.Errorf("failed to start %s kernel: %w", lang, err)
	}
	defer k.Shutdown()

	serverName := fmt.Sprintf("rat-%s", name)
	tracker := activity.New()
	mcpSrv := mcpserver.New(serverName, k, tracker)

	if serveHTTPFlag {
		return runHTTPServer(mcpSrv, servePort, serverName)
	}
	return runStdioServer(mcpSrv, serverName)
}

// loadGenericKernel tries to find a runtime.yaml for the given language
// in the standard search paths and creates a kernel from it.
// Dispatches on kernel.type: "json" (subprocess) or "tmux" (shared session).
func loadGenericKernel(name, lang, cwd, runtimePath string, options map[string]string) (kernel.Kernel, error) {
	configPath, err := findRuntimeConfig(lang)
	if err != nil {
		return nil, fmt.Errorf("language %q: %w\n\nTo add a custom runtime, create ~/.config/rat/runtimes/%s/runtime.yaml\nSee: KERNEL-PROTOCOL.md", lang, err, lang)
	}

	cfg, err := generic.LoadConfig(configPath)
	if err != nil {
		return nil, err
	}

	configDir := filepath.Dir(configPath)

	switch cfg.KernelType() {
	case "tmux":
		return generic.NewTmux(name, cwd, cfg, configDir, runtimePath, options)
	default:
		return generic.New(name, cwd, cfg, configDir, runtimePath, options)
	}
}

// findRuntimeConfig searches for a runtime.yaml in order:
//  1. ~/.config/rat/runtimes/<lang>/runtime.yaml  (user-defined, wins)
//  2. Built-in runtimes embedded in the binary
func findRuntimeConfig(lang string) (string, error) {
	// 1. User-defined takes priority.
	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	userPath := filepath.Join(configDir, "rat", "runtimes", lang, "runtime.yaml")
	if _, err := os.Stat(userPath); err == nil {
		return userPath, nil
	}

	// 2. Built-in: extract from embedded files.
	if runtimes.IsBuiltin(lang) {
		return runtimes.Extract(lang)
	}

	return "", fmt.Errorf("no runtime found for %q\n\nTo add one, create %s\nSee: KERNEL-PROTOCOL.md", lang, userPath)
}

// parseKVFlags turns ["KEY=VALUE", ...] into a map.
func parseKVFlags(flags []string) map[string]string {
	m := make(map[string]string, len(flags))
	for _, f := range flags {
		if k, v, ok := strings.Cut(f, "="); ok {
			m[k] = v
		}
	}
	return m
}

func runStdioServer(mcpSrv *server.MCPServer, name string) error {
	stdio := server.NewStdioServer(mcpSrv)
	fmt.Fprintf(os.Stderr, "%s (stdio)\n", name)
	return stdio.Listen(context.Background(), os.Stdin, os.Stdout)
}

func runHTTPServer(mcpSrv *server.MCPServer, port int, name string) error {
	httpSrv := server.NewStreamableHTTPServer(mcpSrv)

	fmt.Fprintf(os.Stderr, "%s (HTTP)\n", name)
	fmt.Fprintf(os.Stderr, "  MCP: http://127.0.0.1:%d/mcp\n\n", port)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: httpSrv,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutCtx)
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("http: %w", err)
	}
	return nil
}
