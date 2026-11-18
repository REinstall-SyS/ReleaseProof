// The demo deliberately ships two real application versions. They use different
// SQL representations and cannot see each other's application code.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type service struct {
	db      *pgxpool.Pool
	version string
}

type oldUser struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
}

type newUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	version := os.Getenv("APP_VERSION")
	if version != "old" && version != "new" {
		log.Fatal("APP_VERSION must be old or new")
	}
	if os.Getenv("DATABASE_URL") == "" {
		log.Fatal("DATABASE_URL is required")
	}
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal("invalid database connection configuration")
	}
	defer pool.Close()
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	s := &service{db: pool, version: version}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /users/{id}", s.get)
	mux.HandleFunc("POST /users", s.create)
	mux.HandleFunc("PUT /users/{id}", s.update)
	server := &http.Server{Addr: ":" + port, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("demo %s listening on %s", version, server.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func (s *service) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := s.db.Ping(ctx); err != nil {
		respond(w, http.StatusServiceUnavailable, map[string]string{"error": "database unavailable"})
		return
	}
	respond(w, http.StatusOK, map[string]string{"status": "ok", "version": s.version})
}

func userID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		return 0, fmt.Errorf("id must be a positive integer")
	}
	return id, nil
}

func (s *service) get(w http.ResponseWriter, r *http.Request) {
	id, err := userID(r)
	if err != nil {
		respond(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if s.version == "old" {
		u := oldUser{ID: id}
		err = s.db.QueryRow(r.Context(), "SELECT full_name FROM users WHERE id = $1", id).Scan(&u.FullName)
		if err != nil {
			databaseError(w, err)
			return
		}
		respond(w, http.StatusOK, u)
		return
	}
	u := newUser{ID: id}
	err = s.db.QueryRow(r.Context(), "SELECT first_name, last_name FROM users WHERE id = $1", id).Scan(&u.FirstName, &u.LastName)
	if err != nil {
		databaseError(w, err)
		return
	}
	respond(w, http.StatusOK, u)
}

func decode(w http.ResponseWriter, r *http.Request, out any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return fmt.Errorf("body must contain one JSON object")
	}
	return nil
}

func (s *service) write(w http.ResponseWriter, r *http.Request, update bool) {
	var id int64
	var err error
	if update {
		id, err = userID(r)
		if err != nil {
			respond(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	if s.version == "old" {
		var body struct {
			FullName string `json:"full_name"`
		}
		if err = decode(w, r, &body); err != nil || strings.TrimSpace(body.FullName) == "" {
			respond(w, http.StatusBadRequest, map[string]string{"error": "expected nonempty full_name"})
			return
		}
		if update {
			err = s.db.QueryRow(r.Context(), "UPDATE users SET full_name = $1 WHERE id = $2 RETURNING id", body.FullName, id).Scan(&id)
		} else {
			err = s.db.QueryRow(r.Context(), "INSERT INTO users (full_name) VALUES ($1) RETURNING id", body.FullName).Scan(&id)
		}
	} else {
		var body struct {
			FirstName string `json:"first_name"`
			LastName  string `json:"last_name"`
		}
		if err = decode(w, r, &body); err != nil || strings.TrimSpace(body.FirstName) == "" || strings.TrimSpace(body.LastName) == "" {
			respond(w, http.StatusBadRequest, map[string]string{"error": "expected nonempty first_name and last_name"})
			return
		}
		if update {
			err = s.db.QueryRow(r.Context(), "UPDATE users SET first_name = $1, last_name = $2 WHERE id = $3 RETURNING id", body.FirstName, body.LastName, id).Scan(&id)
		} else {
			err = s.db.QueryRow(r.Context(), "INSERT INTO users (first_name, last_name) VALUES ($1, $2) RETURNING id", body.FirstName, body.LastName).Scan(&id)
		}
	}
	if err != nil {
		databaseError(w, err)
		return
	}
	status := http.StatusCreated
	if update {
		status = http.StatusOK
	}
	respond(w, status, map[string]int64{"id": id})
}

func (s *service) create(w http.ResponseWriter, r *http.Request) { s.write(w, r, false) }
func (s *service) update(w http.ResponseWriter, r *http.Request) { s.write(w, r, true) }

func databaseError(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		respond(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}
	// This intentionally returns the real PostgreSQL error to make this small
	// educational demo inspectable. Do not copy this policy into a public API.
	log.Printf("database operation: %v", err)
	respond(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
}

func respond(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
