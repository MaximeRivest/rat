package commands

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"

	"github.com/maximerivest/rat/internal/activity"
	"github.com/maximerivest/rat/internal/bash"
	"github.com/maximerivest/rat/internal/kernel"
	"github.com/maximerivest/rat/internal/mcpserver"
	"github.com/maximerivest/rat/internal/python"
)

var (
	serveHTTPFlag bool
	servePort     int
	serveCwd      string
	serveLangFlag string
	serveNameFlag string
	serveVenvFlag string
)

func init() {
	serveCmd.Flags().BoolVar(&serveHTTPFlag, "http", false, "Run as HTTP server (default: stdio)")
	serveCmd.Flags().IntVar(&servePort, "port", 8720, "HTTP port")
	serveCmd.Flags().StringVar(&serveCwd, "cwd", "", "Working directory (default: current)")
	serveCmd.Flags().StringVar(&serveLangFlag, "lang", "", "Canonical language (for named runtimes)")
	serveCmd.Flags().StringVar(&serveNameFlag, "kernel-name", "", "Runtime name recorded in state (default: first arg)")
	serveCmd.Flags().StringVar(&serveVenvFlag, "venv", "", "Python venv path (py only)")

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

Examples:
  rat serve sh              # MCP stdio server for bash
  rat serve sh --http       # MCP HTTP server on :8720
  rat serve py --http --port 8717 --cwd ~/project`,
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

	// Create the kernel for the requested language.
	var k kernel.Kernel

	switch lang {
	case "sh":
		k, err = bash.New(name, cwd)
	case "py":
		k, err = python.New(name, cwd, serveVenvFlag)
	// Future:
	// case "r":
	//     k, err = rlang.New(cwd)
	// case "ju":
	//     k, err = julia.New(cwd)
	// case "js":
	//     k, err = node.New(cwd)
	default:
		return fmt.Errorf("language %q not yet implemented", lang)
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
