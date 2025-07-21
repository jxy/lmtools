package main

import (
	"context"
	"flag"
	"fmt"
	"lmtools/internal/apiproxy"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	// Parse command-line flags
	var (
		host string
		port int
	)
	flag.StringVar(&host, "host", "0.0.0.0", "Host to bind the server to")
	flag.IntVar(&port, "port", 8082, "Port to bind the server to")
	flag.Parse()

	// Load configuration from environment
	config, err := apiproxy.LoadConfig()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Create and configure server
	server := apiproxy.NewServer(config)

	// Create handler with proper middleware chain
	handler := server

	httpServer := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", host, port),
		Handler:      handler,
		ReadTimeout:  15 * time.Minute,
		WriteTimeout: 15 * time.Minute,
		IdleTimeout:  60 * time.Second,
	}

	// Start server in a goroutine
	go func() {
		log.Printf("Starting API proxy server on %s:%d", host, port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	// Wait for interrupt signal to gracefully shutdown the server
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("Server forced to shutdown: %v", err)
	}

	log.Println("Server exited")
}
