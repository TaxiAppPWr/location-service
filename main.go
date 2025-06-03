package main

import (
	"context"
	"fmt"
	"github.com/gorilla/mux"
	"github.com/redis/go-redis/v9"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

type App struct {
	Router     *mux.Router
	RedisCache *redis.Client
}

func (app *App) Initialize(redisAddr string) error {
	app.RedisCache = redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: "",
	})

	ctx := context.Background()
	_, err := app.RedisCache.Ping(ctx).Result()

	if err != nil {
		return fmt.Errorf("failed to connect to Redis: %w", err)
	}
	log.Println("Connected to Redis")

	_, err = app.RedisCache.ConfigSet(ctx, "notify-keyspace-events", "Ex").Result()
	if err != nil {
		log.Printf("Warning: Could not enable keyspace notifications: %v", err)
		log.Println("Automatic cleanup of expired drivers will not work")
	} else {
		log.Println("Enabled Redis keyspace notifications for expiration events")
		app.setupExpirationListener()
	}

	app.Router = mux.NewRouter()
	app.setupRoutes()
	return nil
}

func (app *App) setupExpirationListener() {
	pubsub := app.RedisCache.PSubscribe(context.Background(), "__keyevent@0__:expired")

	go func() {
		ch := pubsub.Channel()
		for msg := range ch {
			key := msg.Payload
			if strings.HasPrefix(key, DriverPrefix) {
				driverID := strings.TrimPrefix(key, DriverPrefix)

				ctx := context.Background()
				pipe := app.RedisCache.Pipeline()

				pipe.ZRem(ctx, GeoSetKey, driverID)

				pipe.SRem(ctx, ActiveSetKey, driverID)

				_, err := pipe.Exec(ctx)
				if err != nil {
					log.Printf("Error cleaning up expired driver %s: %v", driverID, err)
				}
			}
		}
	}()
}

func (app *App) setupRoutes() {
	app.Router.HandleFunc("/api/location/health", app.HealthCheck).Methods("GET")
	app.Router.HandleFunc("/api/location/drivers/update", app.UpdateDriverLocation).Methods("POST")
	app.Router.HandleFunc("/api/location/drivers/nearby", app.FindNearbyDrivers).Methods("POST")
	app.Router.HandleFunc("/api/location/drivers/{id}", app.GetDriver).Methods("GET")
}

func (app *App) Run(addr string) {
	srv := &http.Server{
		Addr:         addr,
		Handler:      app.Router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  6 * 60 * time.Second,
	}

	go func() {
		log.Printf("Server starting on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	quitSignal := <-quit
	log.Printf("Received shutdown signal: %v", quitSignal)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := app.RedisCache.Close(); err != nil {
		log.Printf("Error closing Redis connection: %v", err)
	}

	log.Println("Shutting down server...")
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}

	log.Println("Server gracefully stopped")
}

func main() {
	app := App{}

	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	if err := app.Initialize(redisAddr); err != nil {
		log.Fatalf("Failed to initialize app: %v", err)
	}

	app.Run(":" + port)
}
