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

	"github.com/maximerivest/rat/internal/bash"
	"github.com/maximerivest/rat/internal/kernel"
	"github.com/maximerivest/rat/internal/mcpserver"
)

var (
	serveHTTPFlag bool
	servePort     int
	serveCwd      string
)

func init() {
	serveCmd.Flags().BoolVar(&serveHTTPFlag, "http", false, "Run as HTTP server (default: stdio)")
	serveCmd.Flags().IntVar(&servePort, "port", 8720, "HTTP port")
	serveCmd.Flags().StringVar(&serveCwd, "cwd", "", "Working directory (default: current)")

	rootCmd.AddCommand(serveCmd)
}

var serveCmd = &cobra.Command{
	Use:   "serve <name>",
	Short: "Start an MCP server for a language kernel",
	Long: `Start an MCP server for a language kernel.

By default, runs as a stdio server (for Claude Desktop, mcp2cli).
With --http, runs as an HTTP server (for shared access from multiple clients).

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
	lang, err := resolveLang(input)
	if err != nil {
		return err
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
		k, err = bash.New(cwd)
	// Future:
	// case "py":
	//     k, err = python.New(cwd, venv)
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

	name := fmt.Sprintf("rat-%s", lang)
	mcpSrv := mcpserver.New(name, k)

	if serveHTTPFlag {
		return runHTTPServer(mcpSrv, servePort, name)
	}
	return runStdioServer(mcpSrv, name)
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
