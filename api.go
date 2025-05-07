package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gorilla/mux"
	"github.com/redis/go-redis/v9"
	"net/http"
	"time"
)

type Driver struct {
	ID        string    `json:"id"`
	IsActive  bool      `json:"isActive"`
	Latitude  float64   `json:"latitude"`
	Longitude float64   `json:"longitude"`
	LastPing  time.Time `json:"lastPing"`
}

type LocationUpdate struct {
	DriverID  string  `json:"driverId"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	IsActive  bool    `json:"isActive"`
}

type NearbyRequest struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Radius    float64 `json:"radius"` // in kilometers
	Limit     int     `json:"limit"`  // limit the number of drivers
}

const (
	GeoSetKey    = "driver_locations" // Geospatial index for driver locations
	DriverPrefix = "driver:"          // Prefix for driver info hash
	ActiveSetKey = "active_drivers"   // Set for tracking active drivers
)

func (app *App) HealthCheck(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()
	_, err := app.RedisCache.Ping(ctx).Result()

	if err != nil {
		respondWithError(w, http.StatusServiceUnavailable, "Redis connection failed")
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]bool{"status": true})
}

func (app *App) UpdateDriverLocation(w http.ResponseWriter, r *http.Request) {
	var update LocationUpdate

	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}

	ctx := context.Background()

	driver := Driver{
		ID:        update.DriverID,
		Latitude:  update.Latitude,
		Longitude: update.Longitude,
		IsActive:  update.IsActive,
		LastPing:  time.Now(),
	}

	pipe := app.RedisCache.Pipeline()

	driverKey := DriverPrefix + update.DriverID
	driverData, err := json.Marshal(driver)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to serialize driver data")
		return
	}

	pipe.Set(ctx, driverKey, driverData, 1*time.Minute)

	pipe.GeoAdd(ctx, GeoSetKey, &redis.GeoLocation{
		Name:      update.DriverID,
		Longitude: update.Longitude,
		Latitude:  update.Latitude,
	})

	if update.IsActive {
		pipe.SAdd(ctx, ActiveSetKey, update.DriverID)
	} else {
		pipe.SRem(ctx, ActiveSetKey, update.DriverID)
	}

	_, err = pipe.Exec(ctx)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, fmt.Sprintf("Redis operation failed: %v", err))
		return
	}

	respondWithJSON(w, http.StatusOK, map[string]string{"message": "Driver location updated successfully"})
}

func (app *App) FindNearbyDrivers(w http.ResponseWriter, r *http.Request) {
	var req NearbyRequest

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}

	// Set default values if not provided
	if req.Radius == 0 {
		req.Radius = 5
	}
	if req.Limit == 0 {
		req.Limit = 10
	}

	ctx := context.Background()

	geoOptions := &redis.GeoRadiusQuery{
		Radius:      req.Radius,
		Unit:        "km",
		WithCoord:   true,
		WithDist:    true,
		WithGeoHash: false,
		Count:       req.Limit,
		Sort:        "ASC",
	}

	locations, err := app.RedisCache.GeoRadius(
		ctx,
		GeoSetKey,
		req.Longitude,
		req.Latitude,
		geoOptions,
	).Result()

	if err != nil {
		respondWithError(w, http.StatusInternalServerError, fmt.Sprintf("Geospatial query failed: %v", err))
		return
	}

	activeDrivers := make([]map[string]interface{}, 0)

	for _, loc := range locations {
		driverID := loc.Name

		isActive, err := app.RedisCache.SIsMember(ctx, ActiveSetKey, driverID).Result()
		if err != nil {
			continue
		}

		if isActive {
			driverKey := DriverPrefix + driverID
			driverData, err := app.RedisCache.Get(ctx, driverKey).Result()
			if err != nil {
				activeDrivers = append(activeDrivers, map[string]interface{}{
					"id":        driverID,
					"distance":  loc.Dist,
					"latitude":  loc.Latitude,
					"longitude": loc.Longitude,
					"isActive":  true,
				})
				continue
			}

			var driver Driver
			if err := json.Unmarshal([]byte(driverData), &driver); err != nil {
				activeDrivers = append(activeDrivers, map[string]interface{}{
					"id":        driverID,
					"distance":  loc.Dist,
					"latitude":  loc.Latitude,
					"longitude": loc.Longitude,
					"isActive":  true,
				})
				continue
			}

			driverWithDist := map[string]interface{}{
				"id":        driver.ID,
				"distance":  loc.Dist, // Distance in km
				"latitude":  driver.Latitude,
				"longitude": driver.Longitude,
				"isActive":  driver.IsActive,
				"lastPing":  driver.LastPing,
			}

			activeDrivers = append(activeDrivers, driverWithDist)
		}
	}

	respondWithJSON(w, http.StatusOK, activeDrivers)
}

func (app *App) GetDriver(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	driverID := vars["id"]

	ctx := context.Background()

	driverKey := DriverPrefix + driverID
	driverData, err := app.RedisCache.Get(ctx, driverKey).Result()

	if errors.Is(err, redis.Nil) {
		respondWithError(w, http.StatusNotFound, "Driver not found")
		return
	} else if err != nil {
		respondWithError(w, http.StatusInternalServerError, fmt.Sprintf("Redis operation failed: %v", err))
		return
	}

	var driver Driver
	if err := json.Unmarshal([]byte(driverData), &driver); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to parse driver data")
		return
	}

	respondWithJSON(w, http.StatusOK, driver)
}
